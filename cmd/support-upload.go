// Copyright (c) 2015-2024 MinIO, Inc.
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
	"fmt"
	"net/url"

	"github.com/minio/cli"
	"github.com/minio/mc/pkg/probe"
	"github.com/minio/pkg/v3/console"
)

// profile command flags.
var (
	uploadFlags = append(globalFlags,
		cli.IntFlag{
			Name:  "issue",
			Usage: "SUBNET issue number to which the file is to be uploaded",
		},
		cli.StringFlag{
			Name:  "comment",
			Usage: "comment to be posted on the issue along with the file",
		},
		cli.BoolFlag{
			Name:  "enc",
			Usage: "accepted for compatibility; uploads are disabled in this build",
		},
		cli.BoolFlag{
			Name:   "dev",
			Usage:  "Development mode",
			Hidden: true,
		},
	)
)

type supportUploadMessage struct {
	Status   string `json:"status"`
	IssueNum int    `json:"-"`
	IssueURL string `json:"issueUrl"`
}

// String colorized upload message
func (s supportUploadMessage) String() string {
	msg := fmt.Sprintf("File uploaded to SUBNET successfully. Click here to visit the issue: %s", subnetIssueURL(s.IssueNum))
	return console.Colorize(supportSuccessMsgTag, msg)
}

// JSON jsonified upload message
func (s supportUploadMessage) JSON() string {
	return toJSON(s)
}

var supportUploadCmd = cli.Command{
	Name:            "upload",
	Usage:           "upload file to a SUBNET issue (disabled in this build)",
	Action:          mainSupportUpload,
	OnUsageError:    onUsageError,
	Before:          setGlobalsFromContext,
	Flags:           uploadFlags,
	HideHelpCommand: true,
	CustomHelpTemplate: `NAME:
  {{.HelpName}} - {{.Usage}}

USAGE:
  {{.HelpName}} [FLAGS] ALIAS FILE

FLAGS:
  {{range .VisibleFlags}}{{.}}
  {{end}}
DESCRIPTION:
  Uploads to MinIO SUBNET are not available in this Silo build of mc.
  The command is retained for CLI compatibility and always exits with an error.

EXIT STATUS:
  1 - SUBNET services are disabled
`,
}

func checkSupportUploadSyntax(ctx *cli.Context) {
	if len(ctx.Args()) != 2 {
		showCommandHelpAndExit(ctx, 1) // last argument is exit code
	}

	if ctx.Int("issue") <= 0 {
		fatal(errDummy().Trace(), "Invalid issue number")
	}
}

// mainSupportUpload is the handle for "mc support upload" command.
func mainSupportUpload(ctx *cli.Context) error {
	if !subnetServicesEnabled() {
		return subnetDisabledExit()
	}
	// Check for command syntax
	checkSupportUploadSyntax(ctx)
	setSuccessMessageColor()

	// Get the alias parameter from cli
	aliasedURL := ctx.Args().Get(0)
	alias, apiKey := initSubnetConnectivity(ctx, aliasedURL, true)
	if len(apiKey) == 0 {
		// api key not passed as flag. Check that the cluster is registered.
		apiKey = validateClusterRegistered(alias, true)
	}

	// Main execution
	execSupportUpload(ctx, alias, apiKey)
	return nil
}

func execSupportUpload(ctx *cli.Context, alias, apiKey string) {
	filePath := ctx.Args().Get(1)
	issueNum := ctx.Int("issue")
	msg := ctx.String("comment")

	params := url.Values{}
	params.Add("issueNumber", fmt.Sprintf("%d", issueNum))
	if len(msg) > 0 {
		params.Add("message", msg)
	}

	uploadURL := SubnetUploadURL("attachment")
	reqURL, headers := prepareSubnetUploadURL(uploadURL, alias, apiKey)

	_, e := (&SubnetFileUploader{
		alias:        alias,
		FilePath:     filePath,
		ReqURL:       reqURL,
		Headers:      headers,
		AutoCompress: true,
		AutoEncrypt:  ctx.Bool("enc"),
		Params:       params,
	}).UploadFileToSubnet()
	if e != nil {
		fatalIf(probe.NewError(e), "Unable to upload file to SUBNET")
	}
	printMsg(supportUploadMessage{IssueNum: issueNum, Status: "success", IssueURL: subnetIssueURL(issueNum)})
}
