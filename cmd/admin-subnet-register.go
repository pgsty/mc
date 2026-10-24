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
	"errors"

	"github.com/minio/cli"
	"github.com/minio/mc/pkg/probe"
)

var adminSubnetRegisterCmd = cli.Command{
	Name:               "register",
	Usage:              "register with MinIO SUBNET (disabled in this build)",
	OnUsageError:       onUsageError,
	Action:             mainAdminRegister,
	Before:             setGlobalsFromContext,
	Hidden:             true,
	CustomHelpTemplate: subnetRegistrationHelpTemplate,
}

func mainAdminRegister(_ *cli.Context) error {
	fatal(probe.NewError(errors.New(subnetDisabledMessage)), "Deprecated command")
	return nil
}
