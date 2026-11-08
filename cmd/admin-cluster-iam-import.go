// Copyright (c) 2022 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"context"
	stdjson "encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zip"
	"github.com/minio/cli"
	"github.com/minio/madmin-go/v3"
	"github.com/minio/mc/pkg/probe"
	"github.com/pgsty/silo-pkg/v3/console"
)

var adminClusterIAMImportCmd = cli.Command{
	Name:            "import",
	Usage:           "imports IAM info from zipped file",
	Action:          mainClusterIAMImport,
	OnUsageError:    onUsageError,
	Before:          setGlobalsFromContext,
	Flags:           globalFlags,
	HideHelpCommand: true,
	CustomHelpTemplate: `NAME:
  {{.HelpName}} - {{.Usage}}

USAGE:
  {{.HelpName}} [FLAGS] TARGET/BUCKET /path/to/mysilo-iam-info.zip

FLAGS:
  {{range .VisibleFlags}}{{.}}
  {{end}}
EXAMPLES:
  1. Set IAM info from previously exported metadata zip file.
     {{.Prompt}} {{.HelpName}} mysilo /tmp/mysilo-iam-info.zip

`,
}

// The SDK uses error interface fields for failed entities, which cannot decode
// SILO's JSON error objects. Keep the wire value intact for reporting.
type iamImportInfo struct {
	Skipped madmin.IAMEntities `json:"skipped,omitempty"`
	Removed madmin.IAMEntities `json:"removed,omitempty"`
	Added   madmin.IAMEntities `json:"added,omitempty"`
	Failed  iamImportFailures  `json:"failed,omitempty"`
}

type iamImportFailure struct {
	Name     string             `json:"name,omitempty"`
	Policies []string           `json:"policies,omitempty"`
	Error    stdjson.RawMessage `json:"error,omitempty"`
}

type iamImportFailures struct {
	Policies        []iamImportFailure `json:"policies,omitempty"`
	Users           []iamImportFailure `json:"users,omitempty"`
	Groups          []iamImportFailure `json:"groups,omitempty"`
	ServiceAccounts []iamImportFailure `json:"serviceAccounts,omitempty"`
	UserPolicies    []iamImportFailure `json:"userPolicies,omitempty"`
	GroupPolicies   []iamImportFailure `json:"groupPolicies,omitempty"`
	STSPolicies     []iamImportFailure `json:"stsPolicies,omitempty"`
}

func importIAMV2(ctx context.Context, client *madmin.AdminClient, source io.Reader) (result iamImportInfo, err error) {
	content, err := io.ReadAll(source)
	if err != nil {
		return result, err
	}
	response, err := client.ExecuteMethod(ctx, http.MethodPut, madmin.RequestData{
		RelPath: "/v3/import-iam-v2", Content: content,
	})
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		return result, err
	}
	if response == nil || response.Body == nil {
		return result, errors.New("empty IAM import response")
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return result, err
	}
	// ExecuteMethod also returns non-200 responses with a nil error.
	if response.StatusCode != http.StatusOK {
		var apiErr madmin.ErrorResponse
		if err := stdjson.Unmarshal(body, &apiErr); err != nil {
			if err := xml.Unmarshal(body, &apiErr); err != nil {
				return result, fmt.Errorf("IAM import returned HTTP %d: %s", response.StatusCode, body)
			}
		}
		if apiErr.Code == "" {
			return result, fmt.Errorf("IAM import returned HTTP %d: %s", response.StatusCode, body)
		}
		if apiErr.Message == "" {
			apiErr.Message = apiErr.Code
		}
		return result, apiErr
	}
	err = stdjson.Unmarshal(body, &result)
	return result, err
}

func (i iamImportInfo) JSON() string {
	output, e := scrubJSONOutput(i)
	fatalIf(probe.NewError(e), "Unable to marshal into JSON.")
	return output
}

func (i iamImportInfo) String() string {
	var messages []string
	messages = append(messages, processIAMEntities(i.Skipped, "Skipped")...)
	messages = append(messages, processIAMEntities(i.Removed, "Removed")...)
	messages = append(messages, processIAMEntities(i.Added, "Added")...)
	messages = append(messages, processErrIAMEntities(i.Failed)...)
	return scrubSecretsFromOutput(strings.Join(messages, "\n"))
}

func processIAMEntities(entities madmin.IAMEntities, action string) []string {
	var messages []string
	if len(entities.Policies) > 0 {
		messages = append(messages, fmt.Sprintf("%s policies: %v", action, strings.Join(entities.Policies, ", ")))
	}
	if len(entities.Users) > 0 {
		messages = append(messages, fmt.Sprintf("%s users: %v", action, strings.Join(entities.Users, ", ")))
	}
	if len(entities.Groups) > 0 {
		messages = append(messages, fmt.Sprintf("%s groups: %v", action, strings.Join(entities.Groups, ", ")))
	}
	if len(entities.ServiceAccounts) > 0 {
		messages = append(messages, fmt.Sprintf("%s service accounts: %v", action, strings.Join(entities.ServiceAccounts, ", ")))
	}
	var users []string
	for _, pol := range entities.UserPolicies {
		for name := range pol {
			users = append(users, name)
		}
	}
	if len(users) > 0 {
		messages = append(messages, fmt.Sprintf("%s policies for users: %v", action, strings.Join(users, ", ")))
	}
	var groups []string
	for _, pol := range entities.GroupPolicies {
		for name := range pol {
			groups = append(groups, name)
		}
	}
	if len(groups) > 0 {
		messages = append(messages, fmt.Sprintf("%s policies for groups: %v", action, strings.Join(groups, ", ")))
	}
	var stsarr []string
	for _, pol := range entities.STSPolicies {
		for name := range pol {
			stsarr = append(stsarr, name)
		}
	}
	if len(stsarr) > 0 {
		messages = append(messages, fmt.Sprintf("%s policies for sts: %v", action, strings.Join(stsarr, ", ")))
	}
	return messages
}

func processErrIAMEntities(entities iamImportFailures) []string {
	var messages []string
	for _, category := range []struct {
		name    string
		entries []iamImportFailure
	}{
		{"policies", entities.Policies},
		{"users", entities.Users},
		{"groups", entities.Groups},
		{"service accounts", entities.ServiceAccounts},
		{"policies for users", entities.UserPolicies},
		{"policies for groups", entities.GroupPolicies},
		{"policies for sts", entities.STSPolicies},
	} {
		for _, entry := range category.entries {
			detail := entry.Name
			if len(entry.Policies) > 0 {
				detail += " (" + strings.Join(entry.Policies, ", ") + ")"
			}
			if len(entry.Error) > 0 {
				var message string
				if stdjson.Unmarshal(entry.Error, &message) != nil {
					message = string(entry.Error)
				}
				detail += ": " + message
			}
			messages = append(messages, fmt.Sprintf("Failed to add %s: %s", category.name, detail))
		}
	}
	return messages
}

func hasIAMImportFailures(entities iamImportFailures) bool {
	return len(entities.Policies) > 0 || len(entities.Users) > 0 || len(entities.Groups) > 0 ||
		len(entities.ServiceAccounts) > 0 || len(entities.UserPolicies) > 0 ||
		len(entities.GroupPolicies) > 0 || len(entities.STSPolicies) > 0
}

func isIAMV2Unsupported(err error) bool {
	var response madmin.ErrorResponse
	if !errors.As(err, &response) {
		return false
	}
	return response.Code == "NotImplemented" || response.Code == "XMinioAdminVersionMismatch"
}

func checkIAMImportSyntax(ctx *cli.Context) {
	if len(ctx.Args()) != 2 {
		showCommandHelpAndExit(ctx, 1) // last argument is exit code
	}
}

// mainClusterIAMImport - iam info import command
func mainClusterIAMImport(ctx *cli.Context) error {
	// Check for command syntax
	checkIAMImportSyntax(ctx)

	// Get the alias parameter from cli
	args := ctx.Args()
	aliasedURL := filepath.ToSlash(args.Get(0))
	aliasedURL = filepath.Clean(aliasedURL)

	var r io.Reader
	var sz int64
	f, e := os.Open(args.Get(1))
	if e != nil {
		fatalIf(probe.NewError(e).Trace(args...), "Unable to get IAM info")
	}
	if st, e := f.Stat(); e == nil {
		sz = st.Size()
	}
	defer f.Close()
	r = f

	_, e = zip.NewReader(r.(io.ReaderAt), sz)
	fatalIf(probe.NewError(e).Trace(args...), fmt.Sprintf("Unable to read zip file %s", args.Get(1)))

	f, e = os.Open(args.Get(1))
	fatalIf(probe.NewError(e).Trace(args...), "Unable to get IAM info")
	defer f.Close()

	// Create a new MinIO Admin Client
	client, err := newAdminClient(aliasedURL)
	if err != nil {
		fatalIf(err.Trace(aliasedURL), "Unable to initialize admin client.")
		return nil
	}

	iamr, e := importIAMV2(globalContext, client, f)
	if e != nil {
		if !isIAMV2Unsupported(e) {
			fatalIf(probe.NewError(e).Trace(aliasedURL), "Unable to import IAM info.")
			return nil
		}
		_, e = f.Seek(0, io.SeekStart)
		fatalIf(probe.NewError(e), "Unable to rewind IAM archive.")
		e = client.ImportIAM(globalContext, f)
		fatalIf(probe.NewError(e).Trace(aliasedURL), "Unable to import IAM info.")
		if !globalJSON {
			console.Infof("IAM info imported to %s from %s\n", aliasedURL, args.Get(1))
		}
	} else {
		printMsg(iamr)
		if hasIAMImportFailures(iamr.Failed) {
			return exitStatus(globalErrorExitStatus)
		}
	}

	return nil
}
