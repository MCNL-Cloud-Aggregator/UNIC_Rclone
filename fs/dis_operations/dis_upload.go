package dis_operations

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/cmd"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/operations"
	rsync "github.com/rclone/rclone/fs/sync"
	"github.com/rclone/rclone/reedsolomon"
	"github.com/spf13/cobra"
)

type UploadTargets struct {
	Remotes   []config.Remote
	UseConfig bool
}

var (
	createEmptySrcDirs = false
)

const (
	uploadRetryAttempts  = 3
	uploadRetryBaseDelay = 700 * time.Millisecond
)

func isTransientUploadError(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	transientMarkers := []string{
		"unexpected end of json input",
		"unexpected eof",
		"eof",
		"timeout",
		"temporary",
		"connection reset",
		"connection refused",
		"connection aborted",
		"broken pipe",
		"too many requests",
		"rate limit",
		"429",
		"500",
		"502",
		"503",
		"504",
	}

	for _, marker := range transientMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func retryUploadOperation(label string, fn func() error) error {
	var lastErr error
	for attempt := 1; attempt <= uploadRetryAttempts; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			if attempt > 1 {
				fmt.Printf("[UNIC Retry] %s succeeded on attempt %d/%d\n", label, attempt, uploadRetryAttempts)
			}
			return nil
		}

		if attempt == uploadRetryAttempts || !isTransientUploadError(lastErr) {
			return lastErr
		}

		delay := uploadRetryBaseDelay * time.Duration(1<<(attempt-1))
		fmt.Printf("[UNIC Retry] %s failed on attempt %d/%d: %v. Retrying in %s...\n",
			label, attempt, uploadRetryAttempts, lastErr, delay)
		time.Sleep(delay)
	}
	return lastErr
}

var copyCommandDefinition = &cobra.Command{
	Use: "copy source:path dest:path",
	Annotations: map[string]string{
		"groups": "Copy,Filter,Listing,Important",
	},
	Run: func(command *cobra.Command, args []string) {
		cmd.CheckArgs(2, 2, command, args)
		fsrc, srcFileName, fdst := cmd.NewFsSrcFileDst(args)
		cmd.RunWithSustainOS(true, true, command, func() error {
			if srcFileName == "" {
				return rsync.CopyDir(context.Background(), fdst, fsrc, createEmptySrcDirs)
			}
			return operations.CopyFile(context.Background(), fdst, fsrc, srcFileName, srcFileName)
		}, true)
	},
}

func Dis_Upload(args []string, target UploadTargets, reSignal bool, loadBalancer LoadBalancerType) error {
	absolutePath, err := dis_init(args[0])
	//fileId := "/" + args[1]
	backendRemote := args[1]
	fileId := args[2]

	if err != nil {
		return err
	}

	//originalFileName := filepath.Base(args[0])
	var distributedFileArray []DistributedFile
	hashedNamesMap := make(map[string]string)

	var sessions map[string]fs.Fs
	if reSignal {
		tempDistributedFileArray, err := GetDistributedFileStruct(fileId)
		if err != nil {
			return err
		}

		for _, dFile := range tempDistributedFileArray {
			if !dFile.Check {
				distributedFileArray = append(distributedFileArray, dFile)
				hashVal, err := CalculateHash(dFile.DistributedFile)
				if err != nil {
					return err
				}
				hashedNamesMap[dFile.DistributedFile] = hashVal
			}
		}

		// Initialize sessions for re-signal path
		remotes := func() []config.Remote {
			if target.UseConfig {
				return config.GetRemotes()
			}
			return target.Remotes
		}()
		sessions, err = initRemoteSessions(remotes, fileId)
		if err != nil {
			return err
		}
		err = MakeDistributionDir(sessions)
		if err != nil {
			return err
		}
	} else {
		// Uncomment this to allow duplicate check
		// Currently commented bc gui not supporting this behavior

		isDuplicate, err := DoesFileStructExist(fileId)
		if err != nil {
			return err
		}

		if isDuplicate {
			// if ShowDescription_DoOverwrite(originalFileName) {
			// 	err = Dis_rm(args, false)
			// 	if err != nil {
			// 		return err
			// 	}
			// } else {
			// 	return nil
			// }
			err = Dis_rm([]string{args[2]}, false)
			if err != nil {
				return err
			}
		}

		hashedNamesMap, distributedFileArray, sessions, err = prepareUpload(absolutePath, backendRemote, fileId, target)
		if err != nil {
			return err
		}
	}

	//// server가 fileID를 발급해서 datamap.json을 완성한 이후 다시 로컬로 보내주는 것 대기
	//fmt.Println("server가 datamap.json을 수정해주기를 대기중...")
	//watcher, err := fsnotify.NewWatcher()
	//if err != nil {
	//	fmt.Printf("%s\n", err)
	//	return err
	//}
	//defer watcher.Close()

	//// 파일이 감지할 파일 지정
	//datamapPath := filepath.Join(GetRcloneDirPath(), "/data/datamap.json")
	//watcher.Add(datamapPath)

	//// 파일 변화 감지
	//select {
	//case event, ok := <-watcher.Events:
	//	if !ok {
	//		fmt.Println("watcher channel close")
	//		return err
	//	}

	//	fmt.Printf("발생한 이벤트: %s\n", event.String())

	//	if event.Has(fsnotify.Write) {
	//		fmt.Printf("write event 발생 %s\n", event.Name)
	//	}

	//case err, ok := <-watcher.Errors:
	//	if !ok {
	//		fmt.Println("watcher channel close")
	//		return err
	//	}
	//	fmt.Printf("%s\n", err)
	//}
	//fmt.Println("server 작업 완료...")

	start := time.Now()
	// Optimized: reduce workerCount from 32 to 8 to avoid cloud rate limits
	if err := startUploadFileGoroutine_Worker(fileId, hashedNamesMap, distributedFileArray, loadBalancer, sessions); err != nil {
		return err
	}

	elapsed := time.Since(start)
	fileInfo, err := os.Stat(args[0])
	if err != nil {
		return err
	}
	throughput := float64(fileInfo.Size()) / elapsed.Seconds() / (1024 * 1024) // MB/s
	currentTime := time.Now().Format("2006-01-02 15:04:05")

	fmt.Printf("Time taken for copy cmd: %s, Throughput: %.2f MB/s, Current Time: %s\n",
		elapsed, throughput, currentTime)

	if err := ResetCheckFlag(fileId); err != nil {
		return err
	}

	fmt.Println("Completed Dis_Upload!")

	// Notify Electron Local Server to upload the config
	electronLocalServerURL := "http://localhost:9090/notify-upload"
	jsonData := []byte(fmt.Sprintf(`{"action": "upload_config", "fileId": "%s"}`, fileId))

	client := http.Client{
		Timeout: 2 * time.Second,
	}

	resp, postErr := client.Post(electronLocalServerURL, "application/json", bytes.NewBuffer(jsonData))
	if postErr != nil {
		fmt.Printf("[UNIC] Error notifying Electron client (App might be closed): %v\n", postErr)
	} else {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			fmt.Println("[UNIC] Successfully notified Electron client to upload config.")
		} else {
			fmt.Printf("[UNIC] Warning: Electron client returned abnormal status code: %d\n", resp.StatusCode)
		}
	}

	return nil
}

func createHashNames(distributedFileArray []DistributedFile) (hashNameMap map[string]string, errors []error) {
	hashNameMap = make(map[string]string)
	var errs []error
	for _, DFile := range distributedFileArray {
		hashedFileName, err := ConvertFileNameForUP(DFile.DistributedFile)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to convert file name %v", err))
			continue // Skip this iteration on error
		}

		hashNameMap[DFile.DistributedFile] = hashedFileName
	}
	return hashNameMap, errs
}

func prepareUpload(absolutePath string, backendRemote string, fileId string, target UploadTargets) (hashNameMap map[string]string, distributedFileInfos []DistributedFile, sessions map[string]fs.Fs, err error) {
	dis_names, checksums, shardSize, padding, shard, parity := reedsolomon.DoEncode(absolutePath, fileId, TryGetPassword())
	fmt.Println("Shard:", shard)
	fmt.Println("Parity:", parity)
	//remotes := config.GetRemotes()
	remotes := func() []config.Remote {
		if target.UseConfig {
			return config.GetRemotes()
		}
		return target.Remotes
	}()

	sessions, err = initRemoteSessions(remotes, fileId)
	if err != nil {
		return nil, nil, nil, err
	}
	err = MakeDistributionDir(sessions)
	if err != nil {
		return nil, nil, nil, err
	}

	// get Distributed info
	for idx, source := range dis_names {
		dis_fileName := filepath.Base(source)

		// Get the distributed info (Remote is filled at distribution-time)
		distributionFile, err := GetDistributedInfo(dis_fileName, Remote{}, checksums[idx], target)
		if err != nil {
			return nil, nil, nil, err
		}

		distributedFileInfos = append(distributedFileInfos, distributionFile)

	}

	hashNameMap, errs := createHashNames(distributedFileInfos)
	if len(errs) > 0 {
		return nil, nil, nil, fmt.Errorf("errors occurred during hashing: %v", errs)
	}

	err = MakeDataMap(absolutePath, backendRemote, fileId, distributedFileInfos, shardSize, padding, shard, parity, remotes)
	if err != nil {
		return nil, nil, nil, err
	}

	return hashNameMap, distributedFileInfos, sessions, nil
}

func uploadFile(fsrc, fdst fs.Fs, shardFileName string, mu *sync.Mutex, totalThroughput *float64, fileCount *int, errs *[]error, fileId string, shardInfo DistributedFile, hashedFileNameMap map[string]string, totalShards int) error {
	// Get file info for throughput calculation
	source := filepath.Join(GetShardPath(), shardFileName)
	fileInfo, err := os.Stat(source)
	if err != nil {
		mu.Lock()
		*errs = append(*errs, fmt.Errorf("error getting file info for %s: %w", source, err))
		mu.Unlock()
		return err
	}
	fileSize := fileInfo.Size()

	// Measure time for upload
	startTime := time.Now()
	// Use operations.CopyFile directly with the reused sessions
	err = retryUploadOperation(
		fmt.Sprintf("copy shard %s to %s", shardFileName, shardInfo.Remote.String()),
		func() error {
			return operations.CopyFile(context.Background(), fdst, fsrc, shardFileName, shardFileName)
		},
	)
	if err != nil {
		mu.Lock()
		*errs = append(*errs, fmt.Errorf("error in operations.CopyFile for file %s: %w", shardFileName, err))
		mu.Unlock()
		return err
	}
	elapsedTime := time.Since(startTime)

	// Calculate throughput
	throughput := float64(fileSize) / elapsedTime.Seconds()
	throughputKbps := throughput * 8 / 1e3

	// Update throughput and file count
	mu.Lock()
	*totalThroughput += throughputKbps
	*fileCount++
	currentFileCount := *fileCount
	mu.Unlock()

	// Notify Progress to Electron App
	go func(current int, total int, speed float64, remote string) {
		client := http.Client{Timeout: 2 * time.Second}
		payload := fmt.Sprintf(`{"action": "progress", "type": "upload", "fileId": "%s", "completedShards": %d, "totalShards": %d, "throughputKbps": %.2f, "remoteName": "%s"}`, fileId, current, total, speed, remote)
		client.Post("http://localhost:9090/notify-upload", "application/json", bytes.NewBuffer([]byte(payload)))
	}(currentFileCount, totalShards, throughputKbps, shardInfo.Remote.String())

	// Update remote info
	err = updateRemoteInfo_Up(fileId, shardInfo, throughputKbps, mu)
	if err != nil {
		return err
	}

	// Erase temp shard
	mu.Lock()
	reedsolomon.DeleteShardWithFileNames([]string{shardFileName})
	mu.Unlock()

	return nil
}

func updateRemoteInfo_Up(fileId string, shardInfo DistributedFile, throughputKbps float64, mu *sync.Mutex) error {
	mu.Lock()
	// Removed: UpdateDistributedFile_CheckFlagAndRemote (Optimized to batch update at the end)
	err := UpdateRemoteInfo(shardInfo.Remote, func(b *RemoteInfo) {
		b.UpdateThroughput(throughputKbps, 0)
	})
	mu.Unlock()
	if err != nil {
		return err
	}
	return nil
}

func startUploadFileGoroutine_Worker(fileId string, hashedFileNameMap map[string]string, distributedFileArray []DistributedFile, loadBalancer LoadBalancerType, sessions map[string]fs.Fs) (err error) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var errs []error
	dir := GetShardPath()
	var totalThroughput float64
	var fileCount int

	// Initialize local source session once
	fsrc, err := fs.NewFs(context.Background(), dir)
	if err != nil {
		return fmt.Errorf("failed to create local source session: %v", err)
	}

	// 1. Group shards by Remote Name
	shardsByRemote := make(map[string][]DistributedFile)
	for _, shard := range distributedFileArray {
		// Allocate Remote first to know where it goes
		if err := shard.AllocateRemote(loadBalancer); err != nil {
			return fmt.Errorf("AllocateRemote error: %v", err)
		}
		shardsByRemote[shard.Remote.String()] = append(shardsByRemote[shard.Remote.String()], shard)
	}

	var completedShards []DistributedFile

	// 2. Start one dedicated goroutine per Remote Provider
	for remoteIdentity, shards := range shardsByRemote {
		fdst, ok := sessions[remoteIdentity]
		if !ok {
			// remoteIdentity is "Name|Type", split to get Name
			remoteName := strings.Split(remoteIdentity, "|")[0]
			// If session doesn't exist for some reason, create it on the fly
			arg := fmt.Sprintf("%s:%s/%s", remoteName, remoteDirectory, fileId)
			fdst = cmd.NewFsDir([]string{arg})
		}

		wg.Add(1)
		go func(identity string, shardList []DistributedFile, remoteFs fs.Fs) {
			defer wg.Done()
			fmt.Printf("[UNIC] Starting batch upload for remote: %s (%d shards)\n", identity, len(shardList))

			for _, shardInfo := range shardList {
				shardFileName := hashedFileNameMap[shardInfo.DistributedFile]

				// Upload file (Sequential within this remote's goroutine)
				err := uploadFile(fsrc, remoteFs, shardFileName, &mu, &totalThroughput, &fileCount, &errs, fileId, shardInfo, hashedFileNameMap, len(distributedFileArray))
				if err != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("upload failed for %s on %s: %w", shardFileName, identity, err))
					mu.Unlock()
					continue // Try next shard anyway
				}

				// Record success
				mu.Lock()
				completedShards = append(completedShards, shardInfo)
				mu.Unlock()
			}
			fmt.Printf("[UNIC] Completed batch upload for remote: %s\n", identity)
		}(remoteIdentity, shards, fdst)
	}

	wg.Wait() // Wait for all provider goroutines to finish

	// 3. Batch update datamap one time
	if len(completedShards) > 0 {
		if batchErr := BatchUpdateDistributedFiles(fileId, completedShards); batchErr != nil {
			mu.Lock()
			errs = append(errs, fmt.Errorf("BatchUpdateDistributedFiles error: %v", batchErr))
			mu.Unlock()
		}
	}

	averageThroughput := float64(0)
	if fileCount > 0 {
		averageThroughput = totalThroughput / float64(fileCount)
	}
	fmt.Printf("Average Throughput: %f Kbps\n", averageThroughput)
	fmt.Println("Current Time:", time.Now().Format("2006-01-02 15:04:05"))

	if len(errs) > 0 {
		return fmt.Errorf("errors occurred: %v", errs)
	}
	return nil
}

func initRemoteSessions(remotes []config.Remote, fileId string) (map[string]fs.Fs, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	sessions := make(map[string]fs.Fs)

	for _, remote := range remotes {
		argument := fmt.Sprintf("%s:%s/%s", remote.Name, remoteDirectory, fileId)
		wg.Add(1)

		go func(remoteName, arg string) {
			defer wg.Done()

			fdst := cmd.NewFsDir([]string{arg})
			mu.Lock()
			sessions[remoteName] = fdst
			mu.Unlock()
		}(remote.Name, argument)
	}

	wg.Wait()
	return sessions, nil
}

func MakeDistributionDir(sessions map[string]fs.Fs) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for name, fdst := range sessions {
		wg.Add(1)

		go func(remoteName string, fsObj fs.Fs) {
			defer wg.Done()

			err := retryUploadOperation(
				fmt.Sprintf("mkdir Distribution/%s on %s", fsObj.Root(), remoteName),
				func() error {
					return operations.Mkdir(context.Background(), fsObj, "")
				},
			)
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("error creating directory on %s: %w", remoteName, err))
				mu.Unlock()
				return
			}
		}(name, fdst)
	}

	wg.Wait()

	if len(errs) > 0 {
		return fmt.Errorf("errors occurred during mkdir: %v", errs)
	}

	return nil
}

func dis_init(arg string) (string, error) {
	// Use the existing getAbsolutePath function to resolve the absolute path
	absolutePath, err := getAbsolutePath(arg)
	if err != nil {
		fmt.Println("Error resolving the absolute path:", err)
		return "", err
	}

	// Check if the file exists
	if _, err := os.Stat(absolutePath); err != nil {
		if os.IsNotExist(err) {
			fmt.Println("File does not exist:", absolutePath)
			return "", fmt.Errorf("file does not exist: %s", absolutePath)
		}
		// Handle other errors (e.g., permission issues)
		fmt.Println("Error checking file:", err)
		return "", err
	}

	// If the file exists, print success message
	fmt.Println("Success: File found at", absolutePath)
	return absolutePath, nil
}
