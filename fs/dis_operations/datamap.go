package dis_operations

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/rclone/rclone/fs/config"
)

var jsonFileMutex sync.Mutex
var datamap_file_name = "datamap.json"

// calculating checksum of file
func calculateChecksum(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file for checksum: %v", err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("failed to compute checksum: %v", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// getting path existing json file
func getJsonFilePath() string {
	path := GetRcloneDirPath()
	return filepath.Join(path, "data", datamap_file_name)
}

// getting rclone dir path
func GetRcloneDirPath() (path string) {
	fullConfigPath := config.GetConfigPath()
	path = filepath.Dir(fullConfigPath)
	return path
}

// reading json file and then returning original file infos
func readJsonFile() (map[string]FileInfo, error) {
	file, err := os.Open(getJsonFilePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[string]FileInfo), nil
		}
		return nil, fmt.Errorf("failed to open JSON file : %v", err)
	}
	defer file.Close()

	var filesMap map[string]FileInfo
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&filesMap)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("failed to decode JSON: %v", err)
	}
	if filesMap == nil {
		filesMap = make(map[string]FileInfo)
	}

	return filesMap, nil

}

// writting original file infos on json file
func writeJsonFile(filePath string, data map[string]FileInfo) error {
	err := os.MkdirAll(filepath.Dir(filePath), os.ModePerm)
	if err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %v", err)
	}

	if err := os.WriteFile(filePath, jsonData, 0644); err != nil {
		return fmt.Errorf("failed to write JSON file: %v", err)
	}
	return nil
}

// making distributed file info
func GetDistributedInfo(fileName string, remote Remote, checksum string, target UploadTargets) (DistributedFile, error) {
	if fileName == "" {
		return DistributedFile{}, errors.New("fileName cannot be empty")
	}

	return DistributedFile{
		DistributedFile: fileName,
		Remote:          remote,
		Checksum:        checksum,
		Check:           false,
		RemotePool:      target.Remotes,
	}, nil
}

// making file info about original file
func MakeDataMap(originalFilePath string, backendRemote string, fileId string, distributedFiles []DistributedFile, disFileSize int64, paddingAmount int64, shard int, parity int, remoteList []config.Remote) error {
	if originalFilePath == "" {
		return errors.New("originalFilePath cannot be empty")
	}

	jsonFilePath := getJsonFilePath()

	originalFileName := filepath.Base(backendRemote)
	originalFileInfo, err := os.Stat(originalFilePath)
	if err != nil {
		return fmt.Errorf("failed to stat original file: %v", err)
	}

	remoteListString := make([]string, len(remoteList))
	for i, remote := range remoteList {
		remoteListString[i] = remote.Name
	}

	checksum, err := calculateChecksum(originalFilePath)
	if err != nil {
		return fmt.Errorf("failed to calculate checksum: %v", err)
	}

	dFileMap := make(map[string]DistributedFile)
	for _, dFile := range distributedFiles {
		dFileMap[dFile.DistributedFile] = dFile
	}

	newFileInfo := FileInfo{
		FileName:             originalFileName,
		FilePath:             backendRemote,
		FileSize:             originalFileInfo.Size(),
		ModTime:              originalFileInfo.ModTime(),
		DisFileSize:          disFileSize,
		Shard:                shard,
		Parity:               parity,
		RemoteList:           remoteListString,
		Flag:                 true,
		State:                "upload",
		Checksum:             checksum,
		Padding:              paddingAmount,
		DistributedFileInfos: dFileMap,
	}

	FilesMap, err := readJsonFile()
	if err != nil {
		return err
	}

	FilesMap[fileId] = newFileInfo
	return writeJsonFile(jsonFilePath, FilesMap)
}

func generateBackendHash(backendRemote string) string {
	backendRemoteHash := sha256.Sum256([]byte(backendRemote))
	backendRemoteHashString := hex.EncodeToString(backendRemoteHash[:])

	return backendRemoteHashString
}

func RemoveFileFromMetadata(fileId string) error {
	filesMap, err := readJsonFile()
	if err != nil {
		return err
	}

	delete(filesMap, fileId)

	return writeJsonFile(getJsonFilePath(), filesMap)
}

func GetFileInfoStruct(fileId string) (FileInfo, error) {
	filesMap, err := readJsonFile()
	if err != nil {
		return FileInfo{}, err
	}

	//backendRemoteHashString := generateBackendHash(backendRemote)
	if fileInfo, exists := filesMap[fileId]; exists {
		fmt.Printf("FileName: %s, FilePath: %s\n", fileInfo.FileName, fileInfo.FilePath)
		return fileInfo, nil
	}

	return FileInfo{}, fmt.Errorf("GetFileInfoStruct file name '%s' not found", fileId)
}

func DoesFileStructExist(fileId string) (bool, error) {
	filesMap, err := readJsonFile()
	if err != nil {
		return false, err
	}

	_, exists := filesMap[fileId]
	return exists, nil
}

func GetDistributedFileStruct(fileId string) ([]DistributedFile, error) {
	filesMap, err := readJsonFile()
	if err != nil {
		return nil, err
	}

	fileInfo, exists := filesMap[fileId]
	for fileInfo_temp, _ := range filesMap {
		fmt.Printf("filesMap FileName: %s, FilePath: %s\n", filesMap[fileInfo_temp].FileName, filesMap[fileInfo_temp].FilePath)
	}
	if !exists {
		return nil, fmt.Errorf("GetDistributedFileStruct file name '%s' not found", fileId)
	}

	disFiles := make([]DistributedFile, 0, len(fileInfo.DistributedFileInfos))
	for _, dFile := range fileInfo.DistributedFileInfos {
		disFiles = append(disFiles, dFile)
	}

	return disFiles, nil
}

// returning checksum of file we want to know
func GetChecksum(fileName string) string {
	fileInfo, err := GetFileInfoStruct(fileName)
	if err != nil {
		return ""
	}
	return fileInfo.Checksum
}

// getting list of checksums about distributed files
func GetChecksumList(name string) (checksums []string) {
	disFiles, err := GetDistributedFileStruct(name)
	if err != nil {
		fmt.Printf("no file data: %v\n", err)
		return
	}
	for _, info := range disFiles {
		checksums = append(checksums, info.Checksum)
	}
	return checksums
}

// returning original file infos without file we want to remove <- 이거 없음
func removeFileByName(files []FileInfo, fileName string) []FileInfo {
	updatedFiles := []FileInfo{}
	for _, file := range files {
		if file.FileName != fileName {
			updatedFiles = append(updatedFiles, file)
		}
	}
	return updatedFiles
}

func calculateRemovableClouds(shard int, parity int, totalClouds int) int {
	if totalClouds == 0 {
		return 0
	}

	fmt.Printf("1: %d, %d %d\n", shard, parity, totalClouds)

	totalShards := shard + parity
	// 1. (shard 수 / 전체 클라우드 수) -> 소수점 올림
	// Shards per cloud (Load)
	shardsPerCloud := math.Ceil(float64(totalShards) / float64(totalClouds))

	fmt.Printf("2: %d\n", shardsPerCloud)
	if shardsPerCloud == 0 {
		return 0
	}

	// 2. (parity 수) / 위 결과 -> 소수점 내림
	removable := math.Floor(float64(parity) / shardsPerCloud)
	fmt.Printf("removable: %d\n", int(removable))
	return int(removable)
}

// DeleteDatamap: 리모트 삭제 후, 복구가 필요한 파일은 즉시 재업로드까지 수행
func DeleteDatamap(remoteName string) error {
	// Set AutoConfirm to bypass stdin prompts which break RCD server mode
	AutoConfirm = true
	defer func() { AutoConfirm = false }()

	// 1. 데이터맵 읽기
	filesMap, err := readJsonFile()
	if err != nil {
		return fmt.Errorf("failed to read datamap: %w", err)
	}

	// 2. 처리 대상 분류
	var unsafeFiles []string
	var safeFiles []string

	for fileId, fileInfo := range filesMap {
		usesRemote := false
		for _, shard := range fileInfo.DistributedFileInfos {
			if shard.Remote.Name == remoteName {
				fmt.Printf("여기 remote: %s\n", remoteName)
				usesRemote = true
				break
			}
		}
		if !usesRemote { // 해당 파일은 삭제하려는 원격 서비스가 없는 거임
			continue
		}
		fmt.Printf("calculate 시작, %d %d %d\n", fileInfo.Shard, fileInfo.Parity, len(fileInfo.RemoteList))
		removable := calculateRemovableClouds(fileInfo.Shard, fileInfo.Parity, len(fileInfo.RemoteList))
		fmt.Printf("removable 값 %d", removable)
		if removable >= 1 { // 복구가 가능하니 reedsolomon 가능 리스트에 첨가
			safeFiles = append(safeFiles, fileId)
		} else {
			unsafeFiles = append(unsafeFiles, fileId)
		}
	}

	fmt.Printf("[DeleteRemote] Analyzing Remote '%s'...\n", remoteName)
	fmt.Printf(" - Metadata Update Only (Safe): %d files\n", len(safeFiles))
	fmt.Printf(" - Full Migration Needed (Unsafe): %d files\n", len(unsafeFiles))

	// ---------------------------------------------------------
	// [Step 1] Unsafe Files 처리 (다운로드 -> 삭제 -> 재업로드)
	// ---------------------------------------------------------
	if len(unsafeFiles) > 0 {
		tempDir := filepath.Join(os.TempDir(), "unic_migration")
		if err := os.MkdirAll(tempDir, 0755); err != nil {
			return fmt.Errorf("failed to create temp dir: %w", err)
		}

		for _, fileId := range unsafeFiles {
			fileInfo, exists := filesMap[fileId]
			if !exists {
				fmt.Printf("   [Warning] File info not found for %s. Skipping.\n", fileId)
				continue
			}

			fmt.Printf("[Migration] Processing unsafe file: %s (Path: %s)\n", fileId, fileInfo.FilePath)

			// 1. 다운로드 (복구)
			// Dis_Download 호출 (인자 3개: ID, 저장폴더, 리모트경로)
			fmt.Printf("인자값: %s %s %s\n", fileId, tempDir, fileInfo.FilePath)
			if err := Dis_Download([]string{fileId, tempDir, fileInfo.FilePath}, false); err != nil {
				fmt.Printf("   [Error] Download failed for %s. Skipping migration: %v\n", fileId, err)
				continue
			}

			// 2. 메타데이터 및 클라우드 파편 삭제 (Dis_rm)
			if err := Dis_rm([]string{fileId}, false); err != nil {
				return fmt.Errorf("failed Dis_rm for %s: %w", fileId, err)
			}

			// ---------------------------------------------------------
			// 3. ★ 재업로드 수행 (Dis_Upload 호출) ★
			// ---------------------------------------------------------
			localTempFilePath := filepath.Join(tempDir, fileInfo.FileName)
			remotePath := fileInfo.FilePath

			// ====================================================================
			// ★ [수정됨] 파일의 RemotePool 데이터를 활용해 활성 클라우드 추출 ★
			// ====================================================================
			fmt.Printf("🔍 추출 작업: '%s' 파일의 RemotePool에서 대상 클라우드를 찾습니다...\n", fileInfo.FileName)

			var validRemotes []config.Remote
			seenRemotes := make(map[string]bool) // 중복 제거용 맵

			// 파일의 파편 정보(DistributedFileInfos)에 기록된 RemotePool을 순회하며
			// 삭제하려는 리모트(remoteName)를 제외한 나머지만 validRemotes에 담습니다.
			for _, dFile := range fileInfo.DistributedFileInfos {
				for _, r := range dFile.RemotePool {
					// 삭제할 리모트가 아니고, 아직 배열에 안 들어간 리모트라면 추가!
					if r.Name != remoteName && !seenRemotes[r.Name] {
						validRemotes = append(validRemotes, r)
						seenRemotes[r.Name] = true
					}
				}
			}

			fmt.Printf("🎯 재업로드 대상 클라우드 확정: %d개\n", len(validRemotes))
			for i, r := range validRemotes {
				fmt.Printf("   - Target %d: %s\n", i+1, r.Name)
			}

			newTargets := UploadTargets{
				Remotes:   validRemotes,
				UseConfig: false,
			}
			// ====================================================================

			fmt.Printf("[3. Upload] Starting Dis_Upload for %s to %d targets...\n", fileId, len(validRemotes))

			// taskID, time 등의 에러가 나던 로깅 변수들 모두 제거
			err := Dis_Upload(
				[]string{localTempFilePath, remotePath, fileId},
				newTargets,
				false,
				RoundRobinFromSelectedRemotes,
			)

			if err != nil {
				fmt.Printf("   ❌ [Error] Upload failed for %s: %v\n", fileId, err)
			} else {
				// 4. 성공 시 임시 파일 삭제 (Cleanup)
				if err := os.Remove(localTempFilePath); err != nil {
					fmt.Printf("   ⚠️ [Warning] Failed to delete temp file %s: %v\n", localTempFilePath, err)
				}
				fmt.Printf("   ✅ [Migration] Completed for %s\n", fileId)
			}
			if err != nil {
				fmt.Printf("   [Error] Upload failed for %s: %v\n", fileId, err)
			} else {
				// 4. 성공 시 임시 파일 삭제 (Cleanup)
				if err := os.Remove(localTempFilePath); err != nil {
					fmt.Printf("   [Warning] Failed to delete temp file %s: %v\n", localTempFilePath, err)
				}
				fmt.Printf("   -> Migration completed for %s\n", fileId)
			}
		}
	}

	// ---------------------------------------------------------
	// [Step 2] Safe Files 처리 (메타데이터 직접 수정)
	// ---------------------------------------------------------
	if len(safeFiles) > 0 {
		if len(unsafeFiles) > 0 {
			filesMap, err = readJsonFile()
			if err != nil {
				return fmt.Errorf("failed to reload datamap: %w", err)
			}
		}

		dirty := false
		for _, fileId := range safeFiles {
			fileInfo, exists := filesMap[fileId]
			if !exists {
				continue
			}

			// 2-1. DistributedFileInfos에서 해당 리모트 파편(Shard) 자체를 삭제
			for key, dFile := range fileInfo.DistributedFileInfos {
				if dFile.Remote.Name == remoteName {
					delete(fileInfo.DistributedFileInfos, key)
				}
			}

			// 2-2. 메인 RemoteList 문자열 배열 갱신
			newRemoteList := []string{}
			for _, rName := range fileInfo.RemoteList {
				if rName != remoteName {
					newRemoteList = append(newRemoteList, rName)
				}
			}
			fileInfo.RemoteList = newRemoteList

			// ★ [추가된 부분] 2-3. 살아있는 파편들의 RemotePool에서 삭제된 리모트 제거 ★
			for key, dFile := range fileInfo.DistributedFileInfos {
				var newPool []config.Remote
				for _, r := range dFile.RemotePool {
					if r.Name != remoteName {
						newPool = append(newPool, r)
					}
				}
				dFile.RemotePool = newPool
				// Go에서 맵의 값(Struct)은 복사본이므로 다시 할당해야 적용됨
				fileInfo.DistributedFileInfos[key] = dFile
			}

			filesMap[fileId] = fileInfo
			dirty = true
			fmt.Printf("[Update] Metadata updated for '%s' (Cleaned RemotePool)\n", fileInfo.FileName)
		}

		if dirty {
			if err := writeJsonFile(getJsonFilePath(), filesMap); err != nil {
				return fmt.Errorf("failed to save updated datamap: %w", err)
			}
		}
	}

	fmt.Println("\nRemote deletion and migration completed successfully.")
	return nil
}

// checking to see if it terminated abnormally and if so, returning what command is was previously
func CheckFlagAndState() (bool, string, string) {
	filesMap, err := readJsonFile()
	if err != nil {
		fmt.Printf("failed to read json file at checkflag func")
	}

	for _, info := range filesMap {
		if info.Flag {
			return info.Flag, info.State, info.FileName
		}
	}
	return false, "", ""
}

// Updating file flag to true.
// this function is used when downloading or deleting a file.
func UpdateFileFlag(fileId string, state string) error {
	jsonFileMutex.Lock()
	defer jsonFileMutex.Unlock()

	filesMap, err := readJsonFile()
	if err != nil {
		return fmt.Errorf("failed to read JSON file: %v", err)
	}

	/*backendRemoteHash := sha256.Sum256([]byte(fileId))
	backendRemoteHashString := hex.EncodeToString(backendRemoteHash[:])*/

	fileInfo, exists := filesMap[fileId]
	if !exists {
		return fmt.Errorf("file '%s' not found\n", fileId)
	}

	fileInfo.Flag = true
	fileInfo.State = state
	filesMap[fileId] = fileInfo

	if err := writeJsonFile(getJsonFilePath(), filesMap); err != nil {
		return fmt.Errorf("failed to write updated JSON: %v", err)
	}

	return nil
}

// updating distributedfile check flag after uploading, downloading or removing
func updateDistributedFile(fileId, distributedFileName string, updateFunc func(*DistributedFile) error) error {
	jsonFileMutex.Lock()
	defer jsonFileMutex.Unlock()

	filesMap, err := readJsonFile()
	if err != nil {
		return fmt.Errorf("failed to read JSON file: %v", err)
	}

	fileInfo, exists := filesMap[fileId]
	if !exists {
		return fmt.Errorf("file '%s' not found", fileId)
	}

	dFile, exists := fileInfo.DistributedFileInfos[distributedFileName]
	if !exists {
		return fmt.Errorf("distributed file '%s' not found for original file '%s'", distributedFileName, fileId)
	}

	// Apply the update function
	if err := updateFunc(&dFile); err != nil {
		return err
	}

	fileInfo.DistributedFileInfos[distributedFileName] = dFile
	filesMap[fileId] = fileInfo

	err = writeJsonFile(getJsonFilePath(), filesMap)
	if err != nil {
		return fmt.Errorf("failed to write updated JSON: %v", err)
	}

	//fmt.Println("Check flag updated!")
	return nil
}

func UpdateDistributedFile_CheckFlag(fileId, distributedFileName string, newCheck bool) error {
	return updateDistributedFile(fileId, distributedFileName, func(dFile *DistributedFile) error {
		dFile.Check = newCheck
		return nil
	})
}

func UpdateDistributedFile_CheckFlagAndRemote(fileId, distributedFileName string, newCheck bool, remote Remote) error {
	return updateDistributedFile(fileId, distributedFileName, func(dFile *DistributedFile) error {
		dFile.Check = newCheck
		dFile.Remote = remote
		return nil
	})
}

// resetting file check flag after finishing operation
func ResetCheckFlag(fileId string) error {
	jsonFileMutex.Lock()
	defer jsonFileMutex.Unlock()

	filesMap, err := readJsonFile()
	if err != nil {
		return fmt.Errorf("failed to read JSON file: %v", err)
	}

	fileInfo, exists := filesMap[fileId]
	if !exists {
		return fmt.Errorf("failed to reset flag: fileId'%s' not found", fileId)
	}

	fileInfo.Flag = false

	for key, dFile := range fileInfo.DistributedFileInfos {
		dFile.Check = false
		fileInfo.DistributedFileInfos[key] = dFile
	}

	filesMap[fileId] = fileInfo

	if err := writeJsonFile(getJsonFilePath(), filesMap); err != nil {
		return fmt.Errorf("failed to write updated JSON: %v", err)
	}

	return nil
}

// input으로 originalName과 hashedFileName []string을 넘겨주면 originalFileName []string넘겨주는 함수
func GetOriginalFileNameList(originalFileName string, hashedFileNameList []string) ([]string, error) {
	filesMap, err := readJsonFile()
	if err != nil {
		return nil, fmt.Errorf("failed to read JSON file: %v", err)
	}

	fileInfo, exists := filesMap[originalFileName]
	if !exists {
		return nil, fmt.Errorf("original file '%s' not found", originalFileName)
	}

	hashToDistributed := make(map[string]string)
	for _, dFile := range fileInfo.DistributedFileInfos {
		calhash, err := CalculateHash(dFile.DistributedFile)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate hash for %q: %v", dFile.DistributedFile, err)
		}
		hashToDistributed[calhash] = dFile.DistributedFile
	}

	var result []string
	for _, hashVal := range hashedFileNameList {
		if distributedName, ok := hashToDistributed[hashVal]; ok {
			result = append(result, distributedName)
		}
	}

	return result, nil

}

// remove하다 멈췄을 때 어떤 파일을 마저 지워야하는지 알려주는 함수
func GetUncompletedFileInfo(fileId string) ([]DistributedFile, error) {
	filesMap, err := readJsonFile()
	if err != nil {
		return nil, fmt.Errorf("failed to read JSON file: %v", err)
	}

	//backendRemoteHashString := generateBackendHash(backendRemote)

	fileInfo, exists := filesMap[fileId]
	if !exists {
		return nil, fmt.Errorf("original file '%s' not found", fileId)
	}

	var uncompleted []DistributedFile

	for _, dFile := range fileInfo.DistributedFileInfos {
		if !dFile.Check && dFile.Remote.String() != "|" {
			uncompleted = append(uncompleted, dFile)
		}
	}

	return uncompleted, nil
}

func GetDatamapFileName() string {
	return datamap_file_name
}
