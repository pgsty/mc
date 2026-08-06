// Copyright (c) 2015-2022 MinIO, Inc.
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
	"encoding/json"

	"github.com/minio/cli"
)

const (
	selfUpdateDisabledMessage         = "Self-update is disabled in this Silo build of mc; upgrade only through your package repository or https://github.com/pgsty/mc/releases."
	selfUpdateTooManyArgumentsMessage = "mc update accepts at most one custom release URL; self-update remains disabled."
)

type disabledUpdateMessage struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (m disabledUpdateMessage) String() string {
	return m.Message
}

func (m disabledUpdateMessage) JSON() string {
	b, _ := json.Marshal(m)
	return string(b)
}

// Keep the command name for CLI compatibility while disabling all release-feed
// and binary-replacement behavior.
func disabledUpdateCmd() cli.Command {
	return cli.Command{
		Name:         "update",
		Usage:        "self-update is disabled in this Silo build",
		Action:       mainUpdate,
		OnUsageError: onUsageError,
		Flags: []cli.Flag{
			cli.BoolFlag{
				Name:  "json",
				Usage: "accepted for compatibility; self-update remains disabled",
			},
		},
		CustomHelpTemplate: `Name:
   {{.HelpName}} - {{.Usage}}

USAGE:
   {{.HelpName}}{{if .VisibleFlags}} [FLAGS]{{end}} [RELEASE-URL]
{{if .VisibleFlags}}
FLAGS:
  {{range .VisibleFlags}}{{.}}
  {{end}}{{end}}

EXIT STATUS:
   1 - self-update is disabled
`,
	}
}

func mainUpdate(ctx *cli.Context) error {
	globalQuiet = ctx.Bool("quiet") || ctx.GlobalBool("quiet")
	globalJSON = ctx.Bool("json") || ctx.GlobalBool("json")

	message := selfUpdateDisabledMessage
	if len(ctx.Args()) > 1 {
		message = selfUpdateTooManyArgumentsMessage
	}
	printMsg(disabledUpdateMessage{Status: "error", Message: message})
	return exitStatus(globalErrorExitStatus)
}
