package dis_operations

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
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
	fmt.Printf("---startDownloadFileGoroutine_Worker start---\n")
	if err := startDownloadFileGoroutine_Worker(distributedFileInfos, fileId, 32, fileInfoForDriveId.DriveIdMap); err != nil {
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

func startDownloadFileGoroutine_Worker(distributedFileInfos []DistributedFile, fileId string, workerCount int, driveIdMap map[string]string) (err error) {
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

	downloader := func() {

		defer func() {
			wg.Done()
		}()

		for fileInfo := range jobs {

			// 실제 다운로드 함수 호출
			err := downloadFile(fileInfo, shardDir, fileId, &mu, &errs, driveIdMap)

			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("failed to download shard from %s: %v", fileInfo.Remote.Name, err))
				mu.Unlock()
			}
		}
	}

	// Start worker goroutines
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go downloader()
	}

	// Send jobs to workers
	for _, fileInfo := range distributedFileInfos {
		jobs <- fileInfo
	}
	close(jobs) // Close channel to signal workers

	wg.Wait() // Wait for all workers to finish

	if len(errs) > 0 {
		return fmt.Errorf("download completed with %d errors. First error: %w", len(errs), errs[0])
	}

	return nil
}

func startDownloadFileGoroutine(distributedFileInfos []DistributedFile, fileId string, driveIdMap map[string]string) (err error) {
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
			if err := downloadFile(fileInfo, shardDir, fileId, &mu, &errs, driveIdMap); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(fileInfo)
	}

	wg.Wait()

	return nil
}

func downloadFile(fileInfo DistributedFile, shardDir, fileId string, mu *sync.Mutex, errs *[]error, driveIdMap map[string]string) error {
	startTime := time.Now()

	hashedFileName, err := CalculateHash(fileInfo.DistributedFile)
	if err != nil {
		mu.Lock()
		*errs = append(*errs, fmt.Errorf("CalculateHash for %s: %w", fileInfo.DistributedFile, err))
		mu.Unlock()
		return err
	}

	var source string

	// 🌟 [핵심 변경] sharing.json(또는 FileInfo)에서 받아온 drive_id가 있는지 확인합니다.
	targetDriveId, hasSharedDriveId := driveIdMap[fileInfo.Remote.Name]

	if hasSharedDriveId && fileInfo.Remote.Type == "onedrive" {
		// 상대방의 drive_id로 직접 접근하도록 Rclone Connection String 문법 적용
		source = fmt.Sprintf("%s,drive_id='%s':%s/%s/%s",
			fileInfo.Remote.Name, targetDriveId, remoteDirectory, fileId, hashedFileName)
		fmt.Printf("[Shared Download] 타겟 Drive ID(%s)로 직접 접근합니다: %s\n", targetDriveId, source)
	} else if len(driveIdMap) > 0 && fileInfo.Remote.Type == "drive" {
		// 🌟 강현님 요청사항: 우선 하드코딩으로 동작하는지 Google Drive API 테스트
		hardcodedGoogleId := "1M7yo0pGJZ-UG4OfM5K_fYuVcGpD53u0c"

		source = fmt.Sprintf("%s,root_folder_id='%s':%s",
			fileInfo.Remote.Name, hardcodedGoogleId, hashedFileName)
		fmt.Printf("[Shared Download] 🧪 Google Drive 하드코딩 테스트 ID(%s)로 직접 접근: %s\n", hardcodedGoogleId, source)
	} else {
		// 공유받은 drive_id가 없거나 일반 리모트일 경우 (기존 방식)
		source = fmt.Sprintf("%s:%s/%s/%s",
			fileInfo.Remote.Name, remoteDirectory, fileId, hashedFileName)
	}

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

		pathPart := srcString
		if colonIdx := strings.Index(srcString, ":"); colonIdx != -1 {
			pathPart = srcString[colonIdx+1:] // ':' 뒷부분만 가져옴
		}

		// 이제 pathPart에는 'Distribution/hash_val' 또는 'hash_val'만 남습니다.
		srcFileName := path.Base(pathPart)

		fmt.Printf("🎯 [Debug] 추출된 정확한 파일명: %s\n", srcFileName)

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
