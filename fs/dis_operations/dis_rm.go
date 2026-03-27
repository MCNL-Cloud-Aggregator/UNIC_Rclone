package dis_operations

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rclone/rclone/cmd"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/operations"
	"github.com/spf13/cobra"
)

var PERM_DEL_FLAG = "--drive-use-trash=false"

func Dis_rm(arg []string, reSignal bool) (err error) {

	fileId := arg[0]
	var distributedFileArray []DistributedFile

	_, err = GetFileInfoStruct(fileId)
	if err != nil {
		return err
	}

	// if re-rm (due to previous failure)
	if reSignal {
		distributedFileArray, err = GetUncompletedFileInfo(fileId)
		if err != nil {
			return err
		}

	} else {
		err = UpdateFileFlag(fileId, "rm")
		if err != nil {
			return err
		}
		fmt.Printf("dis_rm: fileId: %s\n", fileId)
		distributedFileArray, err = GetDistributedFileStruct(fileId)
		if err != nil {
			return err
		}
	}

	start := time.Now()

	if err := startRmFileGoroutine(fileId, distributedFileArray); err != nil {
		return err
	}

	elapsed := time.Since(start)
	fmt.Printf("Time taken for dis_rm: %s\n", elapsed)

	err = ResetCheckFlag(fileId)
	if err != nil {
		return err
	}
	err = RemoveFileFromMetadata(fileId)
	if err != nil {
		return fmt.Errorf("failed to remove file from metadata: %v", err)
	}

	fmt.Printf("Successfully deleted all parts of %s and updated metadata.\n", fileId)

	return nil
}

func remoteCallDeleteFile(args []string) (err error) {
	// 1. 문자열 경로를 Fs와 FileName으로 파싱
	// 예: "dropbox:Distribution/abc" -> (dropbox Fs 객체, "Distribution/abc")
	remotePath := args[1]
	f, fileName := cmd.NewFsFile(remotePath)
	if fileName == "" {
		return fmt.Errorf("invalid file path: %s", remotePath)
	}

	// 2. 해당 백엔드에서 Object(파일 객체)를 찾음
	obj, err := f.NewObject(context.Background(), fileName)
	if err != nil {
		// 이미 파일이 없는 경우 성공으로 간주하거나 에러 처리
		if err == fs.ErrorObjectNotFound {
			return nil
		}
		return err
	}

	// 3. operations.DeleteFile을 사용하여 삭제 실행
	// 이 함수는 내부적으로 각 클라우드의 Remove()를 호출하며,
	// 휴지통 사용 여부 등은 rclone.conf 설정이나 전역 설정을 따릅니다.
	return operations.DeleteFile(context.Background(), obj)
}

func startRmFileGoroutine(fileId string, distributedFileArray []DistributedFile) (err error) {
	var wg sync.WaitGroup
	errCh := make(chan error, len(distributedFileArray))

	remoteDirectory := "Distribution"
	for _, info := range distributedFileArray {
		if info.Remote.String() == "|" {
			fmt.Printf("Empty Remote\n")
			err = UpdateDistributedFile_CheckFlag(fileId, info.DistributedFile, true)
			if err != nil {
				fmt.Printf("UpdateDistributedFile_CheckFlag 에러 : %v\n", err)
			}
			continue
		}

		wg.Add(1)
		go func(info DistributedFile) {
			defer wg.Done()

			hashedFileName, err := CalculateHash(info.DistributedFile)
			if err != nil {
				errCh <- fmt.Errorf("failed to calculate hash %v", err)
			}

			remotePath := fmt.Sprintf("%s:%s/%s/%s", info.Remote.Name, remoteDirectory, fileId, hashedFileName)

			if err := remoteCallDeleteFile([]string{PERM_DEL_FLAG, remotePath}); err != nil {
				errCh <- fmt.Errorf("failed to delete %s on remote %s: %w", info.DistributedFile, info.Remote.Name, err)
			}

			// Update flags
			err = UpdateDistributedFile_CheckFlag(fileId, info.DistributedFile, true)
			if err != nil {
				fmt.Printf("UpdateDistributedFile_CheckFlag 에러 : %v\n", err)
			}

			if err != nil {
				errCh <- fmt.Errorf("error updating remote info: %v", err)
			}
		}(info)
	}

	wg.Wait()
	close(errCh)

	var deleteErrs []error
	for err := range errCh {
		deleteErrs = append(deleteErrs, err)
	}

	if len(deleteErrs) > 0 {
		return fmt.Errorf("errors occurred while deleting files: %v", deleteErrs)
	}

	return nil
}

var deleteFileDefinition = &cobra.Command{
	Use: "deletefile remote:path",
	Annotations: map[string]string{
		"versionIntroduced": "v1.42",
		"groups":            "Important",
	},
	Run: func(command *cobra.Command, args []string) {
		cmd.CheckArgs(1, 1, command, args)
		f, fileName := cmd.NewFsFile(args[0])
		cmd.RunWithSustainOS(true, false, command, func() error {
			if fileName == "" {
				fmt.Printf("%s is a directory or doesn't exist\n", args[0])
				return nil
			}
			fileObj, err := f.NewObject(context.Background(), fileName)
			if err != nil {
				fmt.Printf("%s is a directory or doesn't exist\n", args[0])
				return nil
			}
			return operations.DeleteFile(context.Background(), fileObj)
		}, true)
	},
}
