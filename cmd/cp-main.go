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
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fatih/color"
	"github.com/minio/cli"
	json "github.com/minio/colorjson"
	"github.com/minio/mc/pkg/probe"
	"github.com/minio/minio-go/v7"
	"github.com/pgsty/silo-pkg/v3/console"
)

// cp command flags.
var (
	cpFlags = []cli.Flag{
		cli.StringFlag{
			Name:  "rewind",
			Usage: "roll back object(s) to current version at specified time",
		},
		cli.StringFlag{
			Name:  "version-id, vid",
			Usage: "select an object version to copy",
		},
		cli.BoolFlag{
			Name:  "recursive, r",
			Usage: "copy recursively",
		},
		cli.StringFlag{
			Name:  "older-than",
			Usage: "copy objects older than value in duration string (e.g. 7d10h31s)",
		},
		cli.StringFlag{
			Name:  "newer-than",
			Usage: "copy objects newer than value in duration string (e.g. 7d10h31s)",
		},
		cli.StringFlag{
			Name:  "storage-class, sc",
			Usage: "set storage class for new object(s) on target",
		},
		cli.StringFlag{
			Name:  "attr",
			Usage: "add custom metadata for the object",
		},
		cli.BoolFlag{
			Name:  "preserve, a",
			Usage: "preserve filesystem attributes (mode, ownership, timestamps)",
		},
		cli.BoolFlag{
			Name:  "disable-multipart",
			Usage: "disable multipart upload feature",
		},
		cli.BoolFlag{
			Name:   "md5",
			Usage:  "force all upload(s) to calculate md5sum checksum",
			Hidden: true,
		},
		cli.StringFlag{
			Name:  "tags",
			Usage: "apply one or more tags to the uploaded objects",
		},
		cli.StringFlag{
			Name:  rmFlag,
			Usage: "retention mode to be applied on the object (governance, compliance)",
		},
		cli.StringFlag{
			Name:  rdFlag,
			Usage: "retention duration for the object in d days or y years",
		},
		cli.StringFlag{
			Name:  lhFlag,
			Usage: "apply legal hold to the copied object (on, off)",
		},
		cli.BoolFlag{
			Name:  "zip",
			Usage: "Extract from remote zip file (Silo/MinIO server source only)",
		},
		cli.IntFlag{
			Name:  "max-workers",
			Usage: "maximum number of concurrent copies (default: autodetect)",
		},
		checksumFlag,
	}
)

var (
	rmFlag = "retention-mode"
	rdFlag = "retention-duration"
	lhFlag = "legal-hold"
)

// ErrInvalidMetadata reflects invalid metadata format
var ErrInvalidMetadata = errors.New("specified metadata should be of form key1=value1;key2=value2;... and so on")

// Copy command.
var cpCmd = cli.Command{
	Name:         "cp",
	Usage:        "copy objects",
	Action:       mainCopy,
	OnUsageError: onUsageError,
	Before:       setGlobalsFromContext,
	Flags:        append(append(cpFlags, encFlags...), globalFlags...),
	CustomHelpTemplate: `NAME:
  {{.HelpName}} - {{.Usage}}

USAGE:
  {{.HelpName}} [FLAGS] SOURCE [SOURCE...] TARGET

FLAGS:
  {{range .VisibleFlags}}{{.}}
  {{end}}

ENVIRONMENT VARIABLES:
  MC_ENC_KMS: KMS encryption key in the form of (alias/prefix=key).
  MC_ENC_S3: S3 encryption key in the form of (alias/prefix=key).

EXAMPLES:
  01. Copy a list of objects from local file system to Amazon S3 cloud storage.
      {{.Prompt}} {{.HelpName}} Music/*.ogg s3/jukebox/

  02. Copy a folder recursively from Silo/MinIO cloud storage to Amazon S3 cloud storage.
      {{.Prompt}} {{.HelpName}} --recursive mysilo/mybucket/myfolder/ s3/mybucket/

  03. Copy multiple local folders recursively to Silo/MinIO cloud storage.
      {{.Prompt}} {{.HelpName}} --recursive backup/2014/ backup/2015/ mysilo/archive/

  04. Copy a bucket recursively from aliased Amazon S3 cloud storage to local filesystem on Windows.
      {{.Prompt}} {{.HelpName}} --recursive s3\documents\2014\ C:\Backups\2014

  05. Copy files older than 7 days and 10 hours from Silo/MinIO cloud storage to Amazon S3 cloud storage.
      {{.Prompt}} {{.HelpName}} --older-than 7d10h mysilo/mybucket/myfolder/ s3/mybucket/

  06. Copy files newer than 7 days and 10 hours from Silo/MinIO cloud storage to a local path.
      {{.Prompt}} {{.HelpName}} --newer-than 7d10h mysilo/mybucket/myfolder/ ~/latest/

  07. Copy an object with name containing unicode characters to Amazon S3 cloud storage.
      {{.Prompt}} {{.HelpName}} 本語 s3/andoria/

  08. Copy a local folder with space separated characters to Amazon S3 cloud storage.
      {{.Prompt}} {{.HelpName}} --recursive 'workdir/documents/May 2014/' s3/mycloud

  09. Copy a folder with encrypted objects recursively from Amazon S3 to Silo/MinIO cloud storage using s3 encryption.
      {{.Prompt}} {{.HelpName}} --recursive --enc-s3 "s3/documents" --enc-s3 "mysilo/documents" s3/documents/ mysilo/documents/

  10. Copy a folder with encrypted objects recursively from Amazon S3 to Silo/MinIO cloud storage.
      {{.Prompt}} {{.HelpName}} --recursive --enc-c "s3/documents/=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDA" --enc-c "mysilo/documents/=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5BBB" s3/documents/ mysilo/documents/

  11. Copy a list of objects from local file system to Silo/MinIO cloud storage with specified metadata, separated by ";"
      {{.Prompt}} {{.HelpName}} --attr "key1=value1;key2=value2" Music/*.mp4 mysilo/mybucket/

  12. Copy a folder recursively from Silo/MinIO cloud storage to Amazon S3 cloud storage with Cache-Control and custom metadata, separated by ";".
      {{.Prompt}} {{.HelpName}} --attr "Cache-Control=max-age=90000,min-fresh=9000;key1=value1;key2=value2" --recursive mysilo/mybucket/myfolder/ s3/mybucket/

  13. Copy a text file to an object storage and assign REDUCED_REDUNDANCY storage-class to the uploaded object.
      {{.Prompt}} {{.HelpName}} --storage-class REDUCED_REDUNDANCY myobject.txt mysilo/mybucket

  14. Copy a text file to an object storage and preserve the file system attribute as metadata.
      {{.Prompt}} {{.HelpName}} -a myobject.txt mysilo/mybucket

  15. Copy a text file to an object storage with object lock mode set to 'GOVERNANCE' with retention duration 1 day.
      {{.Prompt}} {{.HelpName}} --retention-mode governance --retention-duration 1d locked.txt mysilo/locked-bucket/

  16. Copy a text file to an object storage with legal-hold enabled.
      {{.Prompt}} {{.HelpName}} --legal-hold on locked.txt mysilo/locked-bucket/

  17. Copy a text file to an object storage and disable multipart upload feature.
      {{.Prompt}} {{.HelpName}} --disable-multipart myobject.txt mysilo/mybucket

  18. Roll back 10 days in the past to copy the content of 'mybucket'
      {{.Prompt}} {{.HelpName}} --rewind 10d -r mysilo/mybucket/ /tmp/dest/

  19. Set tags to the uploaded objects
      {{.Prompt}} {{.HelpName}} -r --tags "category=prod&type=backup" ./data/ mysilo/another-bucket/

`,
}

// copyMessage container for file copy messages
type copyMessage struct {
	Status     string `json:"status"`
	Source     string `json:"source"`
	Target     string `json:"target"`
	Size       int64  `json:"size"`
	TotalCount int64  `json:"totalCount"`
	TotalSize  int64  `json:"totalSize"`
}

// String colorized copy message
func (c copyMessage) String() string {
	return console.Colorize("Copy", fmt.Sprintf("`%s` -> `%s`", c.Source, c.Target))
}

// JSON jsonified copy message
func (c copyMessage) JSON() string {
	c.Status = "success"
	copyMessageBytes, e := json.MarshalIndent(c, "", " ")
	fatalIf(probe.NewError(e), "Unable to marshal into JSON.")

	return string(copyMessageBytes)
}

// Progress - an interface which describes current amount
// of data written.
type Progress interface {
	Get() int64
	SetTotal(int64)
}

// ProgressReader can be used to update the progress of
// an on-going transfer progress.
type ProgressReader interface {
	io.Reader
	Progress
}

// doCopy - Copy a single file from source to destination
func doCopy(ctx context.Context, copyOpts doCopyOpts) URLs {
	if copyOpts.cpURLs.Error != nil {
		copyOpts.cpURLs.Error = copyOpts.cpURLs.Error.Trace()
		return copyOpts.cpURLs
	}

	sourceAlias := copyOpts.cpURLs.SourceAlias
	sourceURL := copyOpts.cpURLs.SourceContent.URL

	if progressReader, ok := copyOpts.pg.(*progressBar); ok {
		progressReader.SetCaption(copyOpts.cpURLs.SourceContent.URL.String() + ":")
	} else if !copyOpts.skipMessage {
		printCopyMessage(copyOpts.cpURLs)
	}

	urls := uploadSourceToTargetURL(ctx, uploadSourceToTargetURLOpts{
		urls:                copyOpts.cpURLs,
		progress:            copyOpts.pg,
		encKeyDB:            copyOpts.encryptionKeys,
		preserve:            copyOpts.preserve,
		isZip:               copyOpts.isZip,
		multipartSize:       copyOpts.multipartSize,
		multipartThreads:    copyOpts.multipartThreads,
		updateProgressTotal: copyOpts.updateProgressTotal,
		ifNotExists:         copyOpts.ifNotExists,
	})
	if copyOpts.isMvCmd && urls.Error == nil {
		rmManager.add(ctx, sourceAlias, sourceURL.String())
	}

	return urls
}

func printCopyMessage(urls URLs) {
	printMsg(copyMessage{
		Source:     filepath.ToSlash(filepath.Join(urls.SourceAlias, urls.SourceContent.URL.Path)),
		Target:     filepath.ToSlash(filepath.Join(urls.TargetAlias, urls.TargetContent.URL.Path)),
		Size:       urls.SourceContent.Size,
		TotalCount: urls.TotalCount,
		TotalSize:  urls.TotalSize,
	})
}

// sameCopyObject compares resolved URLs. Object keys are literal; filesystem
// paths additionally need normalization and file identity (symlinks/hardlinks).
func sameCopyObject(urls URLs) bool {
	source, target := urls.SourceContent.URL, urls.TargetContent.URL
	if source.Type != target.Type {
		return false
	}
	if source.Type == objectStorage {
		return source.Scheme == target.Scheme && strings.EqualFold(source.Host, target.Host) && source.Path == target.Path
	}
	sourcePath, sourceErr := filepath.Abs(source.Path)
	targetPath, targetErr := filepath.Abs(target.Path)
	if sourceErr == nil && targetErr == nil && sourcePath == targetPath {
		return true
	}
	sourceInfo, sourceErr := os.Stat(source.Path)
	targetInfo, targetErr := os.Stat(target.Path)
	return sourceErr == nil && targetErr == nil && os.SameFile(sourceInfo, targetInfo)
}

// checkMoveTarget rejects known self-moves and ambiguous same-key moves through
// different endpoint names. Shared IPs do not prove storage identity (virtual
// hosts can differ), but cannot safely authorize deleting the source either.
// A direct endpoint that cannot be resolved is refused; its copy could not
// connect anyway. A proxied endpoint is resolved by the proxy, so a local lookup
// failure leaves its address unknown and only the literal comparison applies.
func checkMoveTarget(ctx context.Context, urls URLs, resolved map[string]moveEndpointResolution) *probe.Error {
	if sameCopyObject(urls) {
		return errCopyIntoSelf(urls.SourceContent.URL.String()).Trace(urls.TargetContent.URL.String())
	}
	source, target := urls.SourceContent.URL, urls.TargetContent.URL
	if source.Type != objectStorage || target.Type != objectStorage || source.Scheme != target.Scheme || source.Path != target.Path {
		return nil
	}
	sourceURL, targetURL := url.URL{Scheme: source.Scheme, Host: source.Host}, url.URL{Scheme: target.Scheme, Host: target.Host}
	port := func(u url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	if port(sourceURL) != port(targetURL) {
		return nil
	}
	lookup := func(u url.URL) ([]netip.Addr, error) {
		address := net.JoinHostPort(u.Hostname(), port(u))
		if cached, ok := resolved[address]; ok {
			return cached.ips, cached.err
		}
		var result moveEndpointResolution
		if ip, ok := globalResolvers[address]; ok {
			result.ips = []netip.Addr{ip.Unmap()}
		} else if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
			result.ips = []netip.Addr{ip.Unmap()}
		} else {
			lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			result.ips, result.err = net.DefaultResolver.LookupNetIP(lookupCtx, "ip", u.Hostname())
			for i := range result.ips {
				result.ips[i] = result.ips[i].Unmap()
			}
			if result.err != nil && proxied(u) {
				result.ips, result.err = nil, nil
			}
		}
		// Retain failures too: a large listing must not retry a failed DNS
		// lookup separately for every object in the same move session.
		resolved[address] = result
		return result.ips, result.err
	}
	sourceIPs, err := lookup(sourceURL)
	if err != nil {
		return probe.NewError(err).Trace(source.String())
	}
	targetIPs, err := lookup(targetURL)
	if err != nil {
		return probe.NewError(err).Trace(target.String())
	}
	for _, sourceIP := range sourceIPs {
		for _, targetIP := range targetIPs {
			if sourceIP == targetIP {
				return probe.NewError(errors.New("Cannot safely move the same object path between endpoints that share a network address")).Trace(source.String(), target.String())
			}
		}
	}
	return nil
}

type moveEndpointResolution struct {
	ips []netip.Addr
	err error
}

// proxied reports whether S3 transports reach u through an HTTP proxy.
func proxied(u url.URL) bool {
	proxyConfig := environmentProxyConfig()
	proxyURL, err := proxyConfig.ProxyFunc()(&u)
	return err == nil && proxyURL != nil
}

// doCopyFake - Perform a fake copy to update the progress bar appropriately.
func doCopyFake(cpURLs URLs, pg Progress) URLs {
	if progressReader, ok := pg.(*progressBar); ok {
		progressReader.Add64(cpURLs.SourceContent.Size)
	}

	return cpURLs
}

func printCopyURLsError(cpURLs *URLs) {
	// Print in new line and adjust to top so that we
	// don't print over the ongoing scan bar
	if !globalQuiet && !globalJSON {
		console.Eraseline()
	}

	if strings.Contains(cpURLs.Error.ToGoError().Error(),
		" is a folder.") {
		errorIf(cpURLs.Error.Trace(),
			"Folder cannot be copied. Please use `...` suffix.")
	} else {
		errorIf(cpURLs.Error.Trace(),
			"Unable to prepare URL for copying.")
	}
}

func doCopySession(ctx context.Context, cancelCopy context.CancelFunc, cli *cli.Context, encryptionKeys map[string][]prefixSSEPair, isMvCmd bool) error {
	var userMetadata map[string]string
	if attr := cli.String("attr"); attr != "" {
		var err *probe.Error
		userMetadata, err = getMetaDataEntry(attr)
		if err != nil {
			errorIf(err, "Unable to parse --attr.")
			return exitStatus(globalErrorExitStatus)
		}
	}

	var isCopied func(string) bool
	var totalObjects int64

	cpURLsCh := make(chan URLs, 10000)
	errSeen := false

	// Store a progress bar or an accounter
	var pg ProgressReader

	// Enable progress bar reader only during default mode.
	if !globalQuiet && !globalJSON { // set up progress bar
		pg = newProgressBar(0)
	} else {
		pg = newAccounter(0)
	}
	sourceURLs := cli.Args()[:len(cli.Args())-1]
	targetURL := cli.Args()[len(cli.Args())-1] // Last one is target

	// Check if the target path has object locking enabled
	withLock, _ := isBucketLockEnabled(ctx, targetURL)

	isRecursive := cli.Bool("recursive")
	olderThan := cli.String("older-than")
	newerThan := cli.String("newer-than")
	rewind := cli.String("rewind")
	versionID := cli.String("version-id")
	md5, checksum := parseChecksum(cli)
	if withLock {
		// The Content-MD5 header is required for any request to upload an object with a retention period configured using Amazon S3 Object Lock.
		md5, checksum = true, minio.ChecksumNone
	}

	go func() {
		var preparedObjects, preparedBytes int64
		opts := prepareCopyURLsOpts{
			sourceURLs:  sourceURLs,
			targetURL:   targetURL,
			isRecursive: isRecursive,
			encKeyDB:    encryptionKeys,
			olderThan:   olderThan,
			newerThan:   newerThan,
			timeRef:     parseRewindFlag(rewind),
			versionID:   versionID,
			isZip:       cli.Bool("zip"),
		}

		for cpURLs := range prepareCopyURLs(ctx, opts) {
			if cpURLs.Error == nil {
				preparedBytes += cpURLs.SourceContent.Size
				preparedObjects++
				pg.SetTotal(preparedBytes)
			}
			// Transfer immutable scan totals and preparation errors with each
			// result instead of sharing mutable counters across goroutines.
			cpURLs.TotalCount = preparedObjects
			cpURLs.TotalSize = preparedBytes
			select {
			case cpURLsCh <- cpURLs:
			case <-ctx.Done():
				// Drain preparation so its producers can finish after cancellation.
			}
		}
		close(cpURLsCh)
	}()

	quitCh := make(chan struct{})
	statusCh := make(chan URLs)
	parallel := newParallelManager(statusCh, cli.Int("max-workers"))

	go func() {
		moveEndpoints := make(map[string]moveEndpointResolution)
		gracefulStop := func() {
			parallel.stopAndWait()
			close(statusCh)
		}

		for {
			select {
			case <-quitCh:
				for range cpURLsCh {
				}
				gracefulStop()
				return
			case cpURLs, ok := <-cpURLsCh:
				if !ok {
					gracefulStop()
					return
				}

				if ctx.Err() != nil {
					continue
				}
				if cpURLs.Error != nil {
					statusCh <- cpURLs
					continue
				}
				// Compare each resolved destination before announcing or queuing
				// a move, so an identical source can never be removed.
				if isMvCmd {
					if err := checkMoveTarget(ctx, cpURLs, moveEndpoints); err != nil {
						statusCh <- cpURLs.WithError(err)
						continue
					}
				}

				// Initialize target metadata.
				cpURLs.TargetContent.Metadata = make(map[string]string)

				// Initialize target user metadata.
				cpURLs.TargetContent.UserMetadata = make(map[string]string)

				// Check and handle storage class if passed in command line args
				if storageClass := cli.String("storage-class"); storageClass != "" {
					cpURLs.TargetContent.StorageClass = storageClass
				}

				if rm := cli.String(rmFlag); rm != "" {
					cpURLs.TargetContent.RetentionMode = rm
					cpURLs.TargetContent.RetentionEnabled = true
				}
				if rd := cli.String(rdFlag); rd != "" {
					cpURLs.TargetContent.RetentionDuration = rd
				}
				if lh := cli.String(lhFlag); lh != "" {
					cpURLs.TargetContent.LegalHold = strings.ToUpper(lh)
					cpURLs.TargetContent.LegalHoldEnabled = true
				}

				if tags := cli.String("tags"); tags != "" || cli.IsSet("tags") {
					cpURLs.TargetContent.Metadata["X-Amz-Tagging"] = tags
				}

				preserve := cli.Bool("preserve")
				isZip := cli.Bool("zip")
				maps.Copy(cpURLs.TargetContent.UserMetadata, userMetadata)

				cpURLs.MD5 = md5
				cpURLs.checksum = checksum
				cpURLs.DisableMultipart = cli.Bool("disable-multipart")

				// Verify if previously copied, notify progress bar.
				if isCopied != nil && isCopied(cpURLs.SourceContent.URL.String()) {
					parallel.queueTask(func() URLs {
						return doCopyFake(cpURLs, pg)
					}, 0)
				} else {
					// Emit scan totals in dispatch order; concurrent workers may
					// start in a different order and make cumulative values regress.
					if _, ok := pg.(*progressBar); !ok {
						printCopyMessage(cpURLs)
					}
					parallel.queueTask(func() URLs {
						return doCopy(ctx, doCopyOpts{
							cpURLs:         cpURLs,
							pg:             pg,
							encryptionKeys: encryptionKeys,
							isMvCmd:        isMvCmd,
							preserve:       preserve,
							isZip:          isZip,
							skipMessage:    true,
						})
					}, cpURLs.SourceContent.Size)
				}
			}
		}
	}()

	var retErr error
	cpAllFilesErr := true
	canceled := ctx.Done()

loop:
	for {
		select {
		case <-canceled:
			canceled = nil
			retErr = exitStatus(globalCancelExitStatus)
			errSeen = true
			close(quitCh)
			cancelCopy()
			// Receive interrupt notification.
			if !globalQuiet && !globalJSON {
				console.Eraseline()
			}
			// Keep receiving until workers stop, including pending mv deletions.
		case cpURLs, ok := <-statusCh:
			// Status channel is closed, we should return.
			if !ok {
				break loop
			}
			totalObjects = max(totalObjects, cpURLs.TotalCount)
			if cpURLs.Error != nil && cpURLs.SourceContent == nil {
				printCopyURLsError(&cpURLs)
				errSeen = true
				retErr = exitStatus(globalErrorExitStatus)
				continue
			}
			if cpURLs.Error == nil {
				cpAllFilesErr = false
			} else {

				// Set exit status for any copy error
				retErr = exitStatus(globalErrorExitStatus)

				// Print in new line and adjust to top so that we
				// don't print over the ongoing progress bar.
				if !globalQuiet && !globalJSON {
					console.Eraseline()
				}
				errorIf(cpURLs.Error.Trace(cpURLs.SourceContent.URL.String()),
					"Failed to copy `%s`.", cpURLs.SourceContent.URL)
				if isErrIgnored(cpURLs.Error) {
					cpAllFilesErr = false
					continue loop
				}

				errSeen = true
				if progressReader, pgok := pg.(*progressBar); pgok {
					if progressReader.Get() > 0 {
						writeContSize := int(cpURLs.SourceContent.Size)
						totalPGSize := int(atomic.LoadInt64(&progressReader.Total))
						written := int(progressReader.Get())
						if totalPGSize > writeContSize && written > writeContSize {
							progressReader.Set(written - writeContSize)
							progressReader.Update()
						}
					}
				}

			}
		}
	}

	if progressReader, ok := pg.(*progressBar); ok {
		if errSeen || (cpAllFilesErr && totalObjects > 0) {
			// We only erase a line if we are displaying a progress bar
			if !globalQuiet && !globalJSON {
				console.Eraseline()
			}
		} else if progressReader.Get() > 0 {
			progressReader.Finish()
		}
	} else {
		if accntReader, ok := pg.(*accounter); ok {
			stat := accntReader.Stat()
			if errSeen || (cpAllFilesErr && totalObjects > 0) {
				// We only erase a line if we are displaying a progress bar
				if !globalQuiet && !globalJSON {
					console.Eraseline()
				}
			} else {
				printMsg(stat)
			}
		}
	}

	if ctx.Err() != nil {
		return exitStatus(globalCancelExitStatus)
	}

	// Source has error
	if errSeen && retErr == nil {
		retErr = exitStatus(globalErrorExitStatus)
	}

	return retErr
}

// mainCopy is the entry point for cp command.
func mainCopy(cliCtx *cli.Context) error {
	ctx, cancelCopy := context.WithCancel(globalContext)
	defer cancelCopy()

	checkCopySyntax(cliCtx)
	console.SetColor("Copy", color.New(color.FgGreen, color.Bold))

	var err *probe.Error

	// Parse encryption keys per command.
	encryptionKeyMap, err := validateAndCreateEncryptionKeys(cliCtx)
	if err != nil {
		err.Trace(cliCtx.Args()...)
	}
	fatalIf(err, "SSE Error")

	return doCopySession(ctx, cancelCopy, cliCtx, encryptionKeyMap, false)
}

type doCopyOpts struct {
	cpURLs                   URLs
	pg                       ProgressReader
	encryptionKeys           map[string][]prefixSSEPair
	isMvCmd, preserve, isZip bool
	skipMessage              bool
	updateProgressTotal      bool
	multipartSize            string
	multipartThreads         string
	ifNotExists              bool
}
