package dis_operations

import (
	"context"
	"fmt"
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

		hashedNamesMap, distributedFileArray, err = prepareUpload(absolutePath, backendRemote, fileId, target)
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

	if err := startUploadFileGoroutine_Worker(fileId, hashedNamesMap, distributedFileArray, loadBalancer, 32); err != nil {
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

func prepareUpload(absolutePath string, backendRemote string, fileId string, target UploadTargets) (hashNameMap map[string]string, distributedFileInfos []DistributedFile, err error) {
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

	err = MakeDistributionDir(remotes, fileId)
	if err != nil {
		return nil, nil, err
	}

	// get Distributed info
	for idx, source := range dis_names {
		dis_fileName := filepath.Base(source)

		// Get the distributed info (Remote is filled at distribution-time)
		distributionFile, err := GetDistributedInfo(dis_fileName, Remote{}, checksums[idx], target)
		if err != nil {
			return nil, nil, err
		}

		distributedFileInfos = append(distributedFileInfos, distributionFile)

	}

	hashNameMap, errs := createHashNames(distributedFileInfos)
	if len(errs) > 0 {
		return nil, nil, fmt.Errorf("errors occurred during hashing: %v", errs)
	}

	err = MakeDataMap(absolutePath, backendRemote, fileId, distributedFileInfos, shardSize, padding, shard, parity, remotes)
	if err != nil {
		return nil, nil, err
	}

	return hashNameMap, distributedFileInfos, nil
}

func uploadFile(source, dest string, mu *sync.Mutex, totalThroughput *float64, fileCount *int, errs *[]error, fileId string, shardInfo DistributedFile, hashedFileNameMap map[string]string) error {
	// Get file info
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
	err = remoteCallCopy([]string{source, dest})
	if err != nil {
		mu.Lock()
		*errs = append(*errs, fmt.Errorf("error in remoteCallCopy for file %s: %w", source, err))
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
	mu.Unlock()

	// Update remote info
	err = updateRemoteInfo_Up(fileId, shardInfo, throughputKbps, mu)
	if err != nil {
		return err
	}

	// Erase temp shard
	mu.Lock()
	reedsolomon.DeleteShardWithFileNames([]string{hashedFileNameMap[shardInfo.DistributedFile]})
	mu.Unlock()

	return nil
}

func updateRemoteInfo_Up(fileId string, shardInfo DistributedFile, throughputKbps float64, mu *sync.Mutex) error {
	mu.Lock()
	err := UpdateDistributedFile_CheckFlagAndRemote(fileId, shardInfo.DistributedFile, true, shardInfo.Remote)
	if err != nil {
		mu.Unlock()
		return fmt.Errorf("UpdateDistributedFileCheckFlag error: %v", err)
	}
	err = UpdateRemoteInfo(shardInfo.Remote, func(b *RemoteInfo) {
		b.UpdateThroughput(throughputKbps, 0)
	})
	mu.Unlock()
	if err != nil {
		return err
	}
	return nil
}

func startUploadFileGoroutine_Worker(fileId string, hashedFileNameMap map[string]string, distributedFileArray []DistributedFile, loadBalancer LoadBalancerType, workerCount int) (err error) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var errs []error
	dir := GetShardPath()
	var totalThroughput float64
	var fileCount int

	jobs := make(chan DistributedFile, len(distributedFileArray))

	// Worker function
	uploader := func() {
		defer wg.Done()
		for shardInfo := range jobs {
			// Allocate Remote
			mu.Lock()
			err := shardInfo.AllocateRemote(loadBalancer)
			mu.Unlock()
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("AllocateRemote error: %v", err))
				mu.Unlock()
				continue
			}

			dest := fmt.Sprintf("%s:%s/%s", shardInfo.Remote.Name, remoteDirectory, fileId)
			source := filepath.Join(dir, hashedFileNameMap[shardInfo.DistributedFile])

			// Upload file and calculate throughput
			err = uploadFile(source, dest, &mu, &totalThroughput, &fileCount, &errs, fileId, shardInfo, hashedFileNameMap)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}
	}

	// Start worker goroutines
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go uploader()
	}

	// Send jobs to workers
	for _, shardInfo := range distributedFileArray {
		jobs <- shardInfo
	}

	close(jobs) // Close channel to signal workers
	wg.Wait()   // Wait for all workers to finish

	averageThroughput := totalThroughput / float64(fileCount)
	fmt.Printf("Average Throughput: %f Kbps\n", averageThroughput)
	fmt.Println("Current Time:", time.Now().Format("2006-01-02 15:04:05"))

	if len(errs) > 0 {
		return fmt.Errorf("errors occurred: %v", errs)
	}
	return nil
}

func startUploadFileGoroutine(fileId string, hashedFileNameMap map[string]string, distributedFileArray []DistributedFile, loadBalancer LoadBalancerType) (err error) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var errs []error
	dir := GetShardPath()

	var totalThroughput float64 // Accumulates total throughput
	var fileCount int           // Counts number of uploaded files

	for _, shardInfo := range distributedFileArray {
		wg.Add(1)

		go func(shardInfo DistributedFile, loadBalancer LoadBalancerType) {
			defer wg.Done()

			// Allocate Remote
			mu.Lock()
			err := shardInfo.AllocateRemote(loadBalancer)
			mu.Unlock()
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}

			dest := fmt.Sprintf("%s:%s/%s", shardInfo.Remote.Name, remoteDirectory, fileId)
			source := filepath.Join(dir, hashedFileNameMap[shardInfo.DistributedFile])

			// Upload file and calculate throughput
			err = uploadFile(source, dest, &mu, &totalThroughput, &fileCount, &errs, fileId, shardInfo, hashedFileNameMap)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}

		}(shardInfo, loadBalancer)
	}

	wg.Wait()

	averageThroughput := totalThroughput / float64(fileCount)

	fmt.Printf("Average Throughput: %f Kbps\n", averageThroughput)
	fmt.Println("Current Time:", time.Now().Format("2006-01-02 15:04:05"))

	if len(errs) > 0 {
		return fmt.Errorf("errors occurred: %v", errs)
	}
	return nil
}

func MakeDistributionDir(remotes []config.Remote, fileId string) (err error) {
	var wg sync.WaitGroup
	var errs []error
	for _, remote := range remotes {
		argument := fmt.Sprintf("%s:%s/%s", remote.Name, remoteDirectory, fileId)
		wg.Add(1)

		go func(arg string) {
			defer wg.Done()

			err := remoteCallMkdir([]string{arg})
			if err != nil {
				errs = append(errs, fmt.Errorf("error creating directory at %s: %w", arg, err))
				return
			}
		}(argument)
	}

	wg.Wait()

	if len(errs) > 0 {
		return fmt.Errorf("errors occurred: %v", errs)
	}

	return nil
}

// copyCommand.Execute() method를 사용하면 프로그램 전체를 재시작 하는데
// 이 때 CLI 환경을 통해 OS가 넘겨준 명령어를 재실행하게 됨.
// UNIC의 경우 ./rclone mount ... 명령을 재실행하게 되는데
// 이 때 mount가 이미 되어있는 경로에 대해 mount를 다시 한번 실행하므로 오류 발생 및 마운트 해제
// 따라서 copyACommand.Execute()가 아닌 operations.mkdir() method를 실행해야 함.
func remoteCallCopy(args []string) (err error) {
	fmt.Printf("Calling remoteCallCopy with args: %v\n", args)

	//// Fetch the copy command
	//copyCommand := *copyCommandDefinition
	//copyCommand.SetArgs(args)

	//err = copyCommand.Execute()
	//if err != nil {
	//	return fmt.Errorf("error executing copyCommand: %w", err)
	//}

	fsrc, srcFileName, fdst := cmd.NewFsSrcFileDst(args)
	if srcFileName == "" {
		return rsync.CopyDir(context.Background(), fdst, fsrc, createEmptySrcDirs)
	}
	return operations.CopyFile(context.Background(), fdst, fsrc, srcFileName, srcFileName)
}

func logThroughput(totalThroughput float64, fileCount int) {
	if fileCount > 0 {
		averageThroughput := totalThroughput / float64(fileCount)
		fmt.Printf("Average Throughput: %f Kbps\n", averageThroughput)
	}
	fmt.Println("Current Time:", time.Now().Format("2006-01-02 15:04:05"))
}

// copyCommand.Execute() method를 사용하면 프로그램 전체를 재시작 하는데
// 이 때 CLI 환경을 통해 OS가 넘겨준 명령어를 재실행하게 됨.
// UNIC의 경우 ./rclone mount ... 명령을 재실행하게 되는데
// 이 때 mount가 이미 되어있는 경로에 대해 mount를 다시 한번 실행하므로 오류 발생 및 마운트 해제
// 따라서 copyACommand.Execute()가 아닌 operations.mkdir() method를 실행해야 함.
func remoteCallMkdir(args []string) (err error) {
	fmt.Printf("Calling remoteCallMkdir with args: %v\n", args)

	//// Fetch the copy command
	//copyCommand := *mkdirCommandDefinition
	//copyCommand.SetArgs(args)

	//err = copyCommand.Execute()
	//if err != nil {
	//	return fmt.Errorf("error executing mkdirCommand: %w", err)
	//}

	fdst := cmd.NewFsDir(args)
	if !fdst.Features().CanHaveEmptyDirectories && strings.Contains(fdst.Root(), "/") {
		fs.Logf(fdst, "Warning: running mkdir on a remote which can't have empty directories does nothing")
	}

	return operations.Mkdir(context.Background(), fdst, "")
}

var mkdirCommandDefinition = &cobra.Command{
	Use:   "mkdir remote:path",
	Short: `Make the path if it doesn't already exist.`,
	Annotations: map[string]string{
		"groups": "Important",
	},
	Run: func(command *cobra.Command, args []string) {
		cmd.CheckArgs(1, 1, command, args)
		fdst := cmd.NewFsDir(args)
		if !fdst.Features().CanHaveEmptyDirectories && strings.Contains(fdst.Root(), "/") {
			fs.Logf(fdst, "Warning: running mkdir on a remote which can't have empty directories does nothing")
		}
		cmd.RunWithSustainOS(true, false, command, func() error {
			return operations.Mkdir(context.Background(), fdst, "")
		}, true)
	},
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
