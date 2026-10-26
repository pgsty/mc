// Copyright (c) 2026 Feng Ruohang
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/minio/mc/pkg/probe"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/pgsty/silo-pkg/v3/env"
)

// The SDK IAM provider rereads AWS_* on every refresh, even when explicit
// EKSIdentity/Container fields are supplied. Snapshot the selected source
// instead: alias configuration must never modify the process environment or
// make a previously constructed client assume a different identity.
type credentialSource struct {
	mode, endpoint                      string
	tokenFile, roleARN, roleSessionName string
	containerToken, containerTokenFile  string
}

func (credentialSource) Format(w fmt.State, _ rune) {
	_, _ = io.WriteString(w, "[redacted credential source]")
}

func snapshotCredentialSource(alias string) (credentialSource, *probe.Error) {
	source := credentialSource{endpoint: env.Get("MC_STS_ENDPOINT_"+alias, "")}
	if source.endpoint == "" {
		return source, nil
	}
	if !validCredentialEndpoint(source.endpoint) {
		return credentialSource{}, probe.NewError(errors.New("invalid STS endpoint"))
	}
	source.tokenFile = env.Get("MC_WEB_IDENTITY_TOKEN_FILE_"+alias, os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE"))
	switch {
	case source.tokenFile != "":
		source.mode = "web"
		source.roleARN = env.Get("MC_ROLE_ARN_"+alias, os.Getenv("AWS_ROLE_ARN"))
		source.roleSessionName = env.Get("MC_ROLE_SESSION_NAME_"+alias, os.Getenv("AWS_ROLE_SESSION_NAME"))
	case os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "":
		// MC_STS_ENDPOINT overrides the relative URI, as in the SDK IAM provider.
		source.mode = "container"
		source.containerToken = os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN")
	case os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "":
		source.mode = "container"
		source.containerTokenFile = os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE")
		if source.containerTokenFile != "" {
			source.endpoint = os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI")
			if !validCredentialEndpoint(source.endpoint) {
				return credentialSource{}, probe.NewError(errors.New("invalid container credential endpoint"))
			}
		} else {
			source.containerToken = os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN")
		}
	default:
		source.mode = "imds"
	}
	return source, nil
}

func validCredentialEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

func (s credentialSource) isTLS() bool {
	return strings.HasPrefix(s.endpoint, "https://")
}

func (s credentialSource) providers(key clientCacheKey, transport http.RoundTripper) []credentials.Provider {
	var providers []credentials.Provider
	if s.mode != "" {
		if s.mode == "web" && s.roleARN != "" && s.roleSessionName == "" {
			s.roleSessionName = "mc-session-name-" + rand.Text()
		}
		providers = append(providers, &configuredIAM{
			source: s, client: &http.Client{Transport: transport},
		})
	}
	return append(providers, &credentials.Static{Value: credentials.Value{
		AccessKeyID: key.accessKey, SecretAccessKey: key.secretKey,
		SessionToken: key.sessionToken, SignerType: key.signer,
	}})
}

type configuredIAM struct {
	credentials.Expiry
	source credentialSource
	client *http.Client
}

func (p *configuredIAM) Retrieve() (credentials.Value, error) {
	return p.RetrieveWithCredContext(nil)
}

func (p *configuredIAM) RetrieveWithCredContext(cc *credentials.CredContext) (credentials.Value, error) {
	ctx := context.Background()
	if cc != nil && cc.Context != nil {
		ctx = cc.Context
	}
	var value credentials.Value
	var err error
	switch p.source.mode {
	case "web":
		value, err = p.webIdentity(ctx)
	case "container":
		token := p.source.containerToken
		if p.source.containerTokenFile != "" {
			data, e := os.ReadFile(p.source.containerTokenFile)
			if e != nil {
				return value, errors.New("unable to read container credential token")
			}
			token = string(data)
		}
		value, err = p.roleCredentials(ctx, p.source.endpoint, "Authorization", token, false)
	case "imds":
		value, err = p.instanceRole(ctx)
	default:
		return value, errors.New("unsupported credential source")
	}
	if err == nil {
		p.SetExpiration(value.Expiration, credentials.DefaultExpiryWindow)
	}
	return value, err
}

// Do not propagate URL errors or provider response bodies: they can contain
// endpoint passwords, web identity tokens, or reflected credentials.
func (p *configuredIAM) request(ctx context.Context, method, endpoint, header, token, body string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid credential request")
	}
	if header != "" {
		req.Header.Set(header, token)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("credential request failed")
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("credential request returned HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func (p *configuredIAM) webIdentity(ctx context.Context) (credentials.Value, error) {
	token, err := os.ReadFile(p.source.tokenFile)
	if err != nil {
		return credentials.Value{}, errors.New("unable to read web identity token")
	}
	form := url.Values{
		"Action": {"AssumeRoleWithWebIdentity"}, "Version": {credentials.STSVersion},
		"WebIdentityToken": {string(token)},
	}
	if p.source.roleARN != "" {
		form.Set("RoleArn", p.source.roleARN)
		form.Set("RoleSessionName", p.source.roleSessionName)
	}
	resp, err := p.request(ctx, http.MethodPost, p.source.endpoint, "", "", form.Encode())
	if err != nil {
		return credentials.Value{}, err
	}
	defer resp.Body.Close()
	var result credentials.AssumeRoleWithWebIdentityResponse
	if xml.NewDecoder(resp.Body).Decode(&result) != nil {
		return credentials.Value{}, errors.New("invalid web identity response")
	}
	c := result.Result.Credentials
	return credentials.Value{
		AccessKeyID: c.AccessKey, SecretAccessKey: c.SecretKey, SessionToken: c.SessionToken,
		Expiration: c.Expiration, SignerType: credentials.SignatureV4,
	}, nil
}

func (p *configuredIAM) roleCredentials(ctx context.Context, endpoint, header, token string, instance bool) (credentials.Value, error) {
	resp, err := p.request(ctx, http.MethodGet, endpoint, header, token, "")
	if err != nil {
		return credentials.Value{}, err
	}
	defer resp.Body.Close()
	var result struct {
		AccessKeyID, SecretAccessKey, Token, Code string
		Expiration                                time.Time
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil || (instance && result.Code != "Success") {
		return credentials.Value{}, errors.New("invalid role credential response")
	}
	return credentials.Value{
		AccessKeyID: result.AccessKeyID, SecretAccessKey: result.SecretAccessKey, SessionToken: result.Token,
		Expiration: result.Expiration, SignerType: credentials.SignatureV4,
	}, nil
}

func (p *configuredIAM) instanceToken(ctx context.Context) (string, error) {
	tokenCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	resp, err := p.request(tokenCtx, http.MethodPut, p.source.endpoint+credentials.TokenPath, credentials.TokenRequestTTLHeader, credentials.TokenTTL, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		if tokenCtx.Err() != nil {
			return "", tokenCtx.Err()
		}
		return "", errors.New("unable to read instance metadata token")
	}
	return string(data), nil
}

func (p *configuredIAM) instanceRole(ctx context.Context) (credentials.Value, error) {
	token, err := p.instanceToken(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return credentials.Value{}, ctx.Err()
		}
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return credentials.Value{}, err
		}
	}
	u, _ := url.Parse(p.source.endpoint) // Validated by snapshotCredentialSource.
	u.Path = credentials.DefaultIAMSecurityCredsPath
	resp, err := p.request(ctx, http.MethodGet, u.String(), credentials.TokenRequestHeader, token, "")
	if err != nil {
		return credentials.Value{}, err
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return credentials.Value{}, errors.New("unable to read instance role names")
	}
	role, _, _ := strings.Cut(string(data), "\n")
	if role == "" {
		return credentials.Value{}, errors.New("no instance roles available")
	}
	u.Path = path.Join(u.Path, strings.TrimSuffix(role, "\r"))
	return p.roleCredentials(ctx, u.String(), credentials.TokenRequestHeader, token, true)
}
