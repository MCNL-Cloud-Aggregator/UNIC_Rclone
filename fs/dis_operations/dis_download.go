package dis_operations

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/cmd"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/reedsolomon"
	"github.com/spf13/cobra"
)

var copyCommandDefinitionForDown = &cobra.Command{
	Use: "copy source:path dest:path",
	Annotations: map[string]string{
		"groups": "Copy,Filter,Listing,Important",
	},
	Run: func(command *cobra.Command, args []string) {
		cmd.CheckArgs(3, 3, command, args)
		fsrc, srcFileName, fdst := cmd.NewFsSrcFileDst(args)
		cmd.RunWithSustainOS(true, true, command, func() error {
			if srcFileName == "" {
				fmt.Printf("%s is a directory or doesn't exist\n", args[0])
				return nil
			}
			return operations.CopyFile(context.Background(), fdst, fsrc, srcFileName, srcFileName)
		}, true)
	},
}

// 뭐고
func Dis_Download(args []string, reSignal bool) (err error) {

	fileId := args[0]
	fmt.Printf("Dis_Download fileId: %s\n", fileId)
	fileInfoForDriveId, err := GetFileInfoStruct(fileId)
	if err != nil {
		return err
	}

	var distributedFileInfos []DistributedFile

	if reSignal {
		//Get Distribution list(Check 읽어서 false인 것만 들고 오기)
		distributedFileInfos, err = GetUncompletedFileInfo(fileId)
		if err != nil {
			return err
		}

	} else {
		//state 변경
		err = UpdateFileFlag(fileId, "download")
		if err != nil {
			return err
		}
		fmt.Printf("---GetDistributedFileStruct start---\n")
		distributedFileInfos, err = GetDistributedFileStruct(fileId)
		fmt.Printf("distributedFileInfos) DistributedFile: %s, Remote: %s\n", distributedFileInfos[0].DistributedFile, distributedFileInfos[0].Remote)
		fmt.Printf("---GetDistributedFileStruct end---\n")
		if err != nil {
			return err
		}
	}

	start := time.Now()
	fmt.Printf("---initDownloadSessions start---\n")
	sessions, err := initDownloadSessions(distributedFileInfos, fileId, fileInfoForDriveId.DriveIdMap, fileInfoForDriveId.FolderIdMap, fileInfoForDriveId.RemoteTypeMap, fileInfoForDriveId.SharedToEmailMap)
	if err != nil {
		return err
	}
	fmt.Printf("---initDownloadSessions end---\n")

	fmt.Printf("---startDownloadFileGoroutine_Worker start---\n")
	if err := startDownloadFileGoroutine_Worker(distributedFileInfos, fileId, sessions, fileInfoForDriveId.Shard, fileInfoForDriveId.Parity); err != nil {
		return err
	}
	fmt.Printf("---startDownloadFileGoroutine_Worker end---\n")

	elapsed := time.Since(start)
	fmt.Println("Current Time:", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Printf("Time taken for dis_download: %s\n", elapsed)

	absolutePath, err := getAbsolutePath(args[1])
	if err != nil {
		return err
	}

	// Move downloaded file to destination
	fmt.Printf("---GetFileInfoStruct start---\n")
	fileInfo, err := GetFileInfoStruct(fileId)
	fmt.Printf("---GetFileInfoStruct end---\n")
	if err != nil {
		return err
	}

	checksums := make(map[string]string)
	for _, each := range distributedFileInfos {
		checksums[each.DistributedFile] = each.Checksum
	}

	//os.Exit(1)
	fmt.Printf("---DoDecode start---\n")
	fmt.Printf("Dis_Download backendRemote2: %s\n", fileId)

	passwordToUse := fileInfo.Password
	if passwordToUse == "" {
		passwordToUse = TryGetPassword()
	}

	//=originalFileName := filepath.Base(backendRemote)
	fmt.Printf("Dis_Download args[2]: %s\n", args[2])
	err = reedsolomon.DoDecode(fileId, args[2], absolutePath, fileInfo.Padding, checksums, fileInfo.Shard, fileInfo.Parity, passwordToUse)
	if err != nil {
		result := ShowDescription_RemoveFile(fileId, err)
		if result {
			err = Dis_rm([]string{fileId}, false)
			if err != nil {
				return err
			}
		}
		return nil
	}
	fmt.Printf("---DoDecode end---\n")
	notifyDownloadProgress(fileId, len(distributedFileInfos), len(distributedFileInfos), 0, "reed-solomon")

	// change Flag and Check to false
	err = ResetCheckFlag(args[0])
	if err != nil {
		return err
	}

	fmt.Printf("File successfully downloaded to %s\n", absolutePath)

	var distributedFiles []string
	for _, info := range distributedFileInfos {
		distributedFiles = append(distributedFiles, info.DistributedFile)
	}

	reedsolomon.DeleteShardWithFileNames(distributedFiles)

	return nil
}

type downloadSession struct {
	Sources []downloadSource
}

type downloadSource struct {
	Fs         fs.Fs
	ConnString string
}

func initDownloadSessions(shards []DistributedFile, fileId string, driveIdMap, folderIdMap, remoteTypeMap, sharedToEmailMap map[string]string) (map[string]downloadSession, error) {
	sessions := make(map[string]downloadSession)
	uniqueRemotes := make(map[string]DistributedFile)
	for _, s := range shards {
		uniqueRemotes[s.Remote.String()] = s
	}

	for id, s := range uniqueRemotes {
		ownerRemoteName := s.Remote.Name
		remoteType := strings.TrimSpace(remoteTypeMap[ownerRemoteName])
		if remoteType == "" {
			remoteType = s.Remote.Type
		}
		sharedToEmail := strings.TrimSpace(sharedToEmailMap[ownerRemoteName])

		localRemoteName := ownerRemoteName
		hasSharingRemoteMetadata := remoteType != "" && sharedToEmail != ""
		if hasSharingRemoteMetadata {
			var ok bool
			localRemoteName, ok = findLocalRemoteName(remoteType, sharedToEmail)
			if !ok {
				return nil, fmt.Errorf("shared file requires %s remote for %s; please connect that account first", remoteType, sharedToEmail)
			}
		} else {
			if UseSharingJson {
				fmt.Printf("[shared-download] metadata missing remote_type/shared_to_email; falling back to owner remote name\n")
			}
		}

		targetDriveId, hasSharedDriveId := driveIdMap[ownerRemoteName]
		targetFolderId, hasFolderId := folderIdMap[ownerRemoteName]

		defaultConnString := fmt.Sprintf("%s:%s/%s", localRemoteName, remoteDirectory, fileId)
		connStrings := []string{}

		switch {
		case hasSharedDriveId && strings.EqualFold(remoteType, "onedrive"):
			actualDriveId := targetDriveId
			if actualDriveId == "" && strings.Contains(targetFolderId, "!") {
				actualDriveId = strings.Split(targetFolderId, "!")[0]
			}
			connStrings = append(connStrings, fmt.Sprintf("%s,drive_id=%s,root_folder_id=%s:", localRemoteName, actualDriveId, targetFolderId))
		case hasFolderId && strings.EqualFold(remoteType, "drive"):
			connStrings = append(connStrings, fmt.Sprintf("%s,root_folder_id=%s:", localRemoteName, targetFolderId))
		case hasFolderId && strings.EqualFold(remoteType, "dropbox"):
			connStrings = append(connStrings, fmt.Sprintf("%s,root_namespace=%s:", localRemoteName, targetFolderId))
		}
		connStrings = append(connStrings, defaultConnString)

		var lastErr error
		sources := []downloadSource{}
		for _, connString := range connStrings {
			fsrc, err := fs.NewFs(context.Background(), connString)
			// fs.ErrorIsFile can happen if the path is interpreted as a file, but we want the parent FS for copying.
			if err == nil || err == fs.ErrorIsFile {
				lastErr = nil
				sources = append(sources, downloadSource{Fs: fsrc, ConnString: connString})
				continue
			}
			lastErr = err
		}
		if len(sources) == 0 {
			return nil, fmt.Errorf("failed to create session for %s using %s: %w", id, strings.Join(connStrings, ", "), lastErr)
		}
		sessions[id] = downloadSession{Sources: sources}
	}
	return sessions, nil
}

func findLocalRemoteName(remoteType, sharedToEmail string) (string, bool) {
	var matches []string
	for _, remote := range config.GetRemotes() {
		if !strings.EqualFold(remote.Type, remoteType) {
			continue
		}
		email := strings.TrimSpace(config.GetValue(remote.Name, "email"))
		if strings.EqualFold(email, sharedToEmail) {
			matches = append(matches, remote.Name)
		}
	}

	if len(matches) == 0 {
		return "", false
	}

	upstreamSet := make(map[string]bool)
	for _, upstream := range strings.Fields(config.GetValue("unic", "upstreams")) {
		name := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(upstream), ":/"), ":")
		if name != "" {
			upstreamSet[name] = true
		}
	}

	for _, name := range matches {
		if upstreamSet[name] {
			if len(matches) > 1 {
				fmt.Printf("[shared-download] selected local remote %s for %s %s from unic upstreams (candidates: %s)\n", name, remoteType, sharedToEmail, strings.Join(matches, ", "))
			}
			return name, true
		}
	}

	for _, name := range matches {
		if !strings.HasPrefix(name, "my_") {
			if len(matches) > 1 {
				fmt.Printf("[shared-download] selected local remote %s for %s %s from modern names (candidates: %s)\n", name, remoteType, sharedToEmail, strings.Join(matches, ", "))
			}
			return name, true
		}
	}

	return matches[0], true
}

func startDownloadFileGoroutine_Worker(distributedFileInfos []DistributedFile, fileId string, sessions map[string]downloadSession, requiredShards int, parityShards int) (err error) {
	fmt.Printf("\n========================================================\n")
	fmt.Printf("[DL-Pool] 🚀 다운로드 고루틴 시작! (파일 ID: %s, 파편 수: %d)\n", fileId, len(distributedFileInfos))
	fmt.Printf("========================================================\n")

	shardDir, err := reedsolomon.GetShardDir()
	if err != nil {
		return err
	}

	// Initialize local destination session once
	fdst, err := fs.NewFs(context.Background(), shardDir)
	if err != nil {
		return fmt.Errorf("failed to create local destination session: %v", err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	var fileCount int
	totalShards := len(distributedFileInfos)

	// 1. Group shards by Remote
	shardsByRemote := make(map[string][]DistributedFile)
	for _, shard := range distributedFileInfos {
		shardsByRemote[shard.Remote.String()] = append(shardsByRemote[shard.Remote.String()], shard)
	}

	// 2. Start one goroutine per Remote Provider
	for remoteIdentity, shardList := range shardsByRemote {
		session, ok := sessions[remoteIdentity]
		if !ok {
			fmt.Printf("[DL-Pool] ⚠️ Session not found for %s, skipping.\n", remoteIdentity)
			continue
		}

		wg.Add(1)
		go func(identity string, list []DistributedFile, session downloadSession) {
			defer wg.Done()
			fmt.Printf("[DL-Pool] Starting batch download for remote: %s (%d shards)\n", identity, len(list))

			for _, fileInfo := range list {
				err := downloadFile(fileInfo, fdst, session.Sources, shardDir, fileId, &mu, &errs, &fileCount, totalShards)
				if err != nil {
					shardErr := fmt.Errorf("failed shard %s on %s: %v", fileInfo.DistributedFile, identity, err)
					fmt.Printf("[DL-Pool] ❌ %v\n", shardErr)
					mu.Lock()
					errs = append(errs, shardErr)
					mu.Unlock()
				}
			}
			fmt.Printf("[DL-Pool] Completed batch download for remote: %s\n", identity)
		}(remoteIdentity, shardList, session)
	}

	wg.Wait()

	if len(errs) > 0 {
		successfulShards := totalShards - len(errs)
		if successfulShards < requiredShards {
			return fmt.Errorf("download completed with %d errors; only %d/%d shards available, need at least %d data shards", len(errs), successfulShards, totalShards, requiredShards)
		}
		fmt.Printf("[DL-Pool] ⚠️ %d shard(s) failed, but %d/%d shards are available (required: %d, parity: %d). Continuing with Reed-Solomon reconstruction.\n", len(errs), successfulShards, totalShards, requiredShards, parityShards)
	}

	return nil
}

func copyShardWithRcloneCommand(connString, shardDir, shardName string) error {
	if connString == "" {
		return fmt.Errorf("empty source connection string")
	}
	cmd := exec.Command(os.Args[0], "copy", connString, shardDir, "--include", shardName, "--error-on-no-transfer", "--retries", "1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rclone copy fallback failed: %w: %s", err, strings.TrimSpace(out.String()))
	}
	if _, err := os.Stat(filepath.Join(shardDir, shardName)); err != nil {
		return fmt.Errorf("rclone copy fallback completed but shard was not created: %w: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// Legacy function startDownloadFileGoroutine removed as it was replaced by startDownloadFileGoroutine_Worker

func downloadFile(fileInfo DistributedFile, fdst fs.Fs, sources []downloadSource, shardDir string, fileId string, mu *sync.Mutex, errs *[]error, fileCount *int, totalShards int) error {
	startTime := time.Now()

	hashedFileName, err := CalculateHash(fileInfo.DistributedFile)
	if err != nil {
		return err
	}

	fmt.Printf("🚀 [Download] Shard: %s from %s\n", hashedFileName, fileInfo.Remote.Name)

	// Since fsrc might be the base directory or the file itself due to initDownloadSessions logic
	// we handle the source file name
	srcFileName := hashedFileName

	// If the shard used a special share-root connection string, the hashedFileName might not be
	// relative to fsrc in the same way. But based on current logic, shards are in Distribution/fileId/
	// and initDownloadSessions points there for the 'default' case.
	// For shared drives, it points to the share root.

	var sourceFs fs.Fs
	var lastErr error
	for _, source := range sources {
		err = operations.CopyFile(context.Background(), fdst, source.Fs, srcFileName, srcFileName)
		if err != nil {
			fmt.Printf("[Download] CopyFile failed for %s from %s, falling back to rclone copy --include: %v\n", srcFileName, source.ConnString, err)
			if fallbackErr := copyShardWithRcloneCommand(source.ConnString, shardDir, srcFileName); fallbackErr != nil {
				lastErr = fmt.Errorf("CopyFile error from %s: %w; fallback copy error: %v", source.ConnString, err, fallbackErr)
				continue
			}
		} else if _, statErr := os.Stat(filepath.Join(shardDir, srcFileName)); statErr != nil {
			fmt.Printf("[Download] CopyFile returned success but %s is missing, falling back to rclone copy --include: %v\n", srcFileName, statErr)
			if fallbackErr := copyShardWithRcloneCommand(source.ConnString, shardDir, srcFileName); fallbackErr != nil {
				lastErr = fmt.Errorf("CopyFile produced no local file from %s: %v; fallback copy error: %v", source.ConnString, statErr, fallbackErr)
				continue
			}
		}
		sourceFs = source.Fs
		lastErr = nil
		break
	}
	if lastErr != nil {
		return lastErr
	}

	elapsedTime := time.Since(startTime)
	// Calculate throughput
	shardSize := int64(0)
	if sourceFs != nil {
		if obj, err := sourceFs.NewObject(context.Background(), srcFileName); err == nil {
			shardSize = obj.Size()
		}
	}
	throughput := float64(shardSize) / elapsedTime.Seconds()
	throughputKbps := throughput * 8 / 1e3

	if err := ConvertFileNameForDo(hashedFileName, fileInfo.DistributedFile); err != nil {
		return fmt.Errorf("ConvertFileNameForDo failed: %v", err)
	}

	// Update remote info
	err = updateRemoteInfo_Down(fileId, fileInfo, throughputKbps, mu)
	if err != nil {
		return err
	}

	mu.Lock()
	*fileCount++
	currentFileCount := *fileCount
	mu.Unlock()

	notifyDownloadProgress(fileId, currentFileCount, totalShards, throughputKbps, fileInfo.Remote.String())

	return nil
}

func notifyDownloadProgress(fileId string, completedShards int, totalShards int, throughputKbps float64, remoteName string) {
	go func() {
		client := http.Client{Timeout: 2 * time.Second}
		payload := fmt.Sprintf(`{"action": "progress", "type": "download", "fileId": "%s", "completedShards": %d, "totalShards": %d, "throughputKbps": %.2f, "remoteName": "%s"}`, fileId, completedShards, totalShards, throughputKbps, remoteName)
		_, _ = client.Post("http://localhost:9090/notify-upload", "application/json", bytes.NewBuffer([]byte(payload)))
	}()
}

func updateRemoteInfo_Down(fileId string, shardInfo DistributedFile, throughputKbps float64, mu *sync.Mutex) error {
	mu.Lock()
	err := UpdateDistributedFile_CheckFlag(fileId, shardInfo.DistributedFile, true)
	if err != nil {
		mu.Unlock()
		return fmt.Errorf("UpdateDistributedFileCheckFlag error: %v", err)
	}
	err = UpdateRemoteInfo(shardInfo.Remote, func(b *RemoteInfo) {
		b.UpdateThroughput(throughputKbps, 1)
	})
	mu.Unlock()
	if err != nil {
		return err
	}
	return nil
}

func getAbsolutePath(arg string) (string, error) {
	// Check if the path is absolute
	if filepath.IsAbs(arg) {
		// Return the cleaned absolute path
		return filepath.Clean(arg), nil
	}

	// If it's not absolute, resolve relative to the current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current working directory: %v", err)
	}

	// Join and clean the path to get the absolute version
	destinationPath := filepath.Join(cwd, arg)
	return filepath.Clean(destinationPath), nil
}

// Legacy function remoteCallCopyforDown removed as it was replaced by direct operations.CopyFile calls
