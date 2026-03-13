package dis_operations

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/rclone/rclone/cmd"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/operations"
	fsync "github.com/rclone/rclone/fs/sync"
	"github.com/rclone/rclone/reedsolomon"
	"github.com/spf13/cobra"
)

var copyCommandDefinitionForDown = &cobra.Command{
	Use: "copy source:path dest:path",
	Annotations: map[string]string{
		"groups": "Copy,Filter,Listing,Important",
	},
	Run: func(command *cobra.Command, args []string) {
		cmd.CheckArgs(2, 2, command, args)
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
	_, err = GetFileInfoStruct(fileId)
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
	fmt.Printf("---startDownloadFileGoroutine_Worker start---\n")
	if err := startDownloadFileGoroutine_Worker(distributedFileInfos, fileId, 32); err != nil {
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
	//originalFileName := filepath.Base(backendRemote)
	err = reedsolomon.DoDecode(fileId, args[2], absolutePath, fileInfo.Padding, checksums, fileInfo.Shard, fileInfo.Parity, TryGetPassword())
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

func startDownloadFileGoroutine_Worker(distributedFileInfos []DistributedFile, fileId string, workerCount int) (err error) {
	fmt.Printf("\n========================================================\n")
	fmt.Printf("[DL-Pool] 🚀 다운로드 워커 풀 시작! (파일 ID: %s, 워커 수: %d, 파편 수: %d)\n", fileId, workerCount, len(distributedFileInfos))
	fmt.Printf("========================================================\n")

	shardDir, err := reedsolomon.GetShardDir()
	if err != nil {
		fmt.Printf("[DL-Pool] ❌ 임시 폴더(ShardDir) 가져오기 실패: %v\n", err)
		return err
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	jobs := make(chan DistributedFile, len(distributedFileInfos))

	// ★ 워커 함수 수정: workerID를 받아서 누가 어떤 일을 하는지 추적합니다.
	downloader := func(workerID int) {
		fmt.Printf("[DL-Worker-%d] 🟢 워커 생성됨! 대기열(Jobs)에서 작업 기다리는 중...\n", workerID)

		defer func() {
			fmt.Printf("[DL-Worker-%d] 🔴 워커 종료됨! (wg.Done 호출)\n", workerID)
			wg.Done()
		}()

		for fileInfo := range jobs {
			fmt.Printf("[DL-Worker-%d] 📥 작업 시작: Remote '%s' 에서 파편 다운로드 시도 (경로: %s)\n", workerID, fileInfo.Remote.Name, fileInfo.DistributedFile)

			jobStart := time.Now()

			// 실제 다운로드 함수 호출
			err := downloadFile(fileInfo, shardDir, fileId, &mu, &errs)

			if err != nil {
				fmt.Printf("[DL-Worker-%d] ❌ 작업 실패: Remote '%s' (소요시간: %v) - 사유: %v\n", workerID, fileInfo.Remote.Name, time.Since(jobStart), err)
				mu.Lock()
				errs = append(errs, fmt.Errorf("failed to download shard from %s: %v", fileInfo.Remote.Name, err))
				mu.Unlock()
			} else {
				fmt.Printf("[DL-Worker-%d] ✅ 작업 성공: Remote '%s' 다운로드 완료 (소요시간: %v)\n", workerID, fileInfo.Remote.Name, time.Since(jobStart))
			}
		}
	}

	// Start worker goroutines
	fmt.Printf("[DL-Pool] 👷 워커 %d개 생성 시작...\n", workerCount)
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go downloader(i) // 워커 번호(0, 1, 2...)를 넘겨줍니다.
	}

	// Send jobs to workers
	fmt.Printf("[DL-Pool] 📤 작업 대기열(Channel)에 %d개 파편 할당 중...\n", len(distributedFileInfos))
	for i, fileInfo := range distributedFileInfos {
		jobs <- fileInfo
		fmt.Printf("[DL-Pool] 📋 %d번째 파편 할당 완료: %s\n", i+1, fileInfo.Remote.Name)
	}

	fmt.Printf("[DL-Pool] 🔒 작업 할당 완료. Jobs 채널 닫음 (Close)\n")
	close(jobs) // Close channel to signal workers

	fmt.Printf("[DL-Pool] ⏳ 모든 워커가 끝날 때까지 대기합니다... (wg.Wait 시작)\n")
	waitStart := time.Now()
	wg.Wait() // Wait for all workers to finish
	fmt.Printf("[DL-Pool] 🔓 wg.Wait() 통과 완료! 모든 워커 정상 복귀! (대기시간: %v)\n", time.Since(waitStart))

	if len(errs) > 0 {
		fmt.Printf("[DL-Pool] ⚠️ 다운로드 풀 종료 (에러 %d개 발생)\n", len(errs))
		return fmt.Errorf("download completed with %d errors. First error: %w", len(errs), errs[0])
	}

	fmt.Printf("[DL-Pool] 🎉 다운로드 풀 완벽하게 종료 (에러 없음)\n\n")
	return nil
}

func startDownloadFileGoroutine(distributedFileInfos []DistributedFile, fileId string) (err error) {
	shardDir, err := reedsolomon.GetShardDir()
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for _, fileInfo := range distributedFileInfos {
		wg.Add(1)
		go func(fileInfo DistributedFile) {
			defer wg.Done()
			if err := downloadFile(fileInfo, shardDir, fileId, &mu, &errs); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(fileInfo)
	}

	wg.Wait()

	return nil
}

func downloadFile(fileInfo DistributedFile, shardDir, fileId string, mu *sync.Mutex, errs *[]error) error {
	startTime := time.Now()

	hashedFileName, err := CalculateHash(fileInfo.DistributedFile)
	if err != nil {
		mu.Lock()
		*errs = append(*errs, fmt.Errorf("CalculateHash for %s: %w", fileInfo.DistributedFile, err))
		mu.Unlock()
		return err
	}

	source := fmt.Sprintf("%s:%s/%s", fileInfo.Remote.Name, remoteDirectory, hashedFileName)
	fmt.Printf("Downloading shard %s to %s\n", source, shardDir)
	downloadedFilePath := path.Join(shardDir, hashedFileName)
	fmt.Printf("downloadedFilePath: %s\n", downloadedFilePath)

	if err := remoteCallCopyforDown([]string{source, shardDir}); err != nil {
		mu.Lock()
		*errs = append(*errs, fmt.Errorf("remoteCallCopyforDown for %s: %w", fileInfo.DistributedFile, err))
		mu.Unlock()
		return err
	}

	elapsedTime := time.Since(startTime)
	downloadedFile, err := os.Stat(downloadedFilePath)
	if err != nil {
		mu.Lock()
		*errs = append(*errs, fmt.Errorf("downloaded file %s does not exist", downloadedFilePath))
		mu.Unlock()
		return err
	}

	// Calculate throughput
	throughput := float64(downloadedFile.Size()) / elapsedTime.Seconds()
	throughputKbps := throughput * 8 / 1e3

	if err := ConvertFileNameForDo(hashedFileName, fileInfo.DistributedFile); err != nil {
		return fmt.Errorf("ConvertFileNameForDo for %s: %w", fileInfo.DistributedFile, err)
	}

	// Update remote info
	err = updateRemoteInfo_Down(fileId, fileInfo, throughputKbps, mu)
	if err != nil {
		return err
	}

	return nil
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

func remoteCallCopyforDown(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("invalid arguments: %v", args)
	}

	ctx := context.Background()
	srcString := args[0] // 예: my_dropbox:Distribution/hash_val
	dstString := args[1] // 예: /home/kali/.config/rclone/shard

	fmt.Printf("🚀 [Pure API Copy] Starting download from %s to %s\n", srcString, dstString)

	// 1. 목적지(Destination) 파일시스템(Fs) 객체 생성 (로컬 폴더)
	fdst, err := fs.NewFs(ctx, dstString)
	if err != nil {
		return fmt.Errorf("failed to create destination FS: %w", err)
	}

	// 2. 소스(Source) 파일시스템(Fs) 객체 생성 (클라우드 파편)
	fsrc, err := fs.NewFs(ctx, srcString)

	// ★ Rclone 엔진의 핵심: 소스 경로가 '파일'인 경우 ErrorIsFile 에러를 뱉음 ★
	if err == fs.ErrorIsFile {
		// 이 경우 fsrc는 파일이 들어있는 '부모 폴더'가 됩니다.
		// 경로의 맨 마지막 부분(파일명)을 추출해서 단일 파일 복사를 수행합니다.
		srcFileName := path.Base(srcString)

		// Cobra 알바생을 거치지 않고 Rclone 심장부(API)에 직접 파일 복사 지시!
		err = operations.CopyFile(ctx, fdst, fsrc, srcFileName, srcFileName)
		if err != nil {
			return fmt.Errorf("failed to copy file using pure API: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to create source FS: %w", err)
	}

	// 3. 만약 파일이 아니라 '폴더' 형태로 들어왔을 경우 (예외 처리)
	err = fsync.CopyDir(ctx, fdst, fsrc, false)
	if err != nil {
		return fmt.Errorf("failed to copy directory using pure API: %w", err)
	}

	return nil
}
