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
var UseSharingJson bool = false // Set to true to read from sharing.json

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
	if UseSharingJson {
		return filepath.Join(path, "data", "sharing.json")
	}
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
	jsonFileMutex.Lock()
	defer jsonFileMutex.Unlock()
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

// DeleteDatamap: 리모트(클라우드) 삭제 시, 해당 리모트에 저장된 파편들을 확인하고
// 데이터 복구가 필요한 파일(Unsafe)은 다운로드 후 다른 리모트에 재업로드하며,
// 복구가 필요 없는 파일(Safe)은 메타데이터(데이터맵)만 업데이트하는 함수입니다.
func DeleteDatamap(remoteName string) error {
	// RCD(Remote Control Daemon) 서버 모드에서 표준 입력(stdin) 프롬프트가 뜨면
	// 프로세스가 멈추는 것을 방지하기 위해 자동 확인(AutoConfirm) 플래그를 활성화합니다.
	AutoConfirm = true
	// 함수가 종료될 때 AutoConfirm 설정을 원래대로(false) 되돌립니다.
	defer func() { AutoConfirm = false }()

	// 1. 데이터맵(datamap.json) 읽기
	// 현재 저장된 모든 파일의 메타데이터 정보를 메모리로 불러옵니다.
	filesMap, err := readJsonFile()
	if err != nil {
		return fmt.Errorf("failed to read datamap: %w", err) // 읽기 실패 시 에러 반환
	}

	// 2. 파일 처리 대상 분류
	// 삭제될 리모트로 인해 파일이 손상될 위험이 있는 파일(unsafe)과
	// 여전히 복구 가능한 상태인 파일(safe)을 담을 배열을 선언합니다.
	var unsafeFiles []string
	var safeFiles []string

	// 데이터맵에 있는 모든 파일을 순회하면서 삭제할 리모트에 파편이 있는지 확인합니다.
	for fileId, fileInfo := range filesMap {
		usesRemote := false
		// 해당 파일의 분산 저장된 파편 정보들을 확인합니다.
		for _, shard := range fileInfo.DistributedFileInfos {
			// 파편이 저장된 리모트 이름이 삭제하려는 리모트 이름과 같은지 확인
			if shard.Remote.Name == remoteName {
				fmt.Printf("여기 remote: %s\n", remoteName)
				usesRemote = true // 삭제하려는 리모트를 사용 중임을 표시
				break
			}
		}
		
		// 삭제하려는 리모트에 파편이 없다면, 이 파일은 영향을 받지 않으므로 다음 파일로 넘어갑니다.
		if !usesRemote {
			continue
		}
		
		fmt.Printf("calculate 시작, %d %d %d\n", fileInfo.Shard, fileInfo.Parity, len(fileInfo.RemoteList))
		// 현재 활성화된 리모트 개수에서 하나(삭제될 리모트)를 뺐을 때,
		// 남은 리모트들만으로 Reed-Solomon 복구가 가능한지 계산합니다.
		removable := calculateRemovableClouds(fileInfo.Shard, fileInfo.Parity, len(fileInfo.RemoteList))
		fmt.Printf("removable 값 %d", removable)
		
		if removable >= 1 {
			// 여유분(removable)이 1 이상이면 이 리모트가 삭제되어도 파일 복구가 가능하므로 safe 배열에 추가
			safeFiles = append(safeFiles, fileId)
		} else {
			// 여유분이 부족하여 파일이 손상될 위험이 있으므로 재업로드가 필요한 unsafe 배열에 추가
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
		// 파일을 임시로 다운로드하여 복구해둘 시스템의 임시 디렉토리 경로 생성
		tempDir := filepath.Join(os.TempDir(), "unic_migration")
		// 임시 디렉토리가 없으면 생성 (권한 0755)
		if err := os.MkdirAll(tempDir, 0755); err != nil {
			return fmt.Errorf("failed to create temp dir: %w", err)
		}

		// 재업로드가 필요한 파일들을 순회하며 마이그레이션 진행
		for _, fileId := range unsafeFiles {
			fileInfo, exists := filesMap[fileId]
			if !exists {
				fmt.Printf("   [Warning] File info not found for %s. Skipping.\n", fileId)
				continue
			}

			fmt.Printf("[Migration] Processing unsafe file: %s (Path: %s)\n", fileId, fileInfo.FilePath)

			// 1. 다운로드 (복구)
			// 삭제 예정인 리모트를 포함하여 기존 파편들을 모아 원본 파일로 복구(다운로드)합니다.
			fmt.Printf("인자값: %s %s %s\n", fileId, tempDir, fileInfo.FilePath)
			// Dis_Download는 ID, 임시저장폴더, 원격경로를 배열로 받음
			if err := Dis_Download([]string{fileId, tempDir, fileInfo.FilePath}, false); err != nil {
				fmt.Printf("   [Error] Download failed for %s. Skipping migration: %v\n", fileId, err)
				continue // 다운로드 실패 시 해당 파일의 마이그레이션 건너뜀
			}

			// 2. 메타데이터 및 클라우드 파편 삭제
			// 복구된 원본이 생겼으므로, 기존 파편들과 메타데이터를 클라우드에서 삭제합니다.
			if err := Dis_rm([]string{fileId}, false); err != nil {
				return fmt.Errorf("failed Dis_rm for %s: %w", fileId, err)
			}

			// ---------------------------------------------------------
			// 3. ★ 재업로드 수행 (새로운 리모트 구성으로 업로드) ★
			// ---------------------------------------------------------
			// 임시 디렉토리에 복구된 원본 파일의 로컬 경로
			localTempFilePath := filepath.Join(tempDir, fileInfo.FileName)
			remotePath := fileInfo.FilePath // 원격 저장소 상의 경로

			fmt.Printf("🔍 추출 작업: '%s' 파일의 RemotePool에서 대상 클라우드를 찾습니다...\n", fileInfo.FileName)

			// 재업로드할 때 사용할 클라우드(리모트) 목록을 구합니다.
			var validRemotes []config.Remote
			seenRemotes := make(map[string]bool) // 중복 추가 방지용 맵

			// 기존 파일의 파편 정보(DistributedFileInfos)에 기록된 RemotePool을 순회
			for _, dFile := range fileInfo.DistributedFileInfos {
				for _, r := range dFile.RemotePool {
					// 삭제될 리모트가 아니고, 아직 validRemotes 배열에 안 들어간 리모트만 추가합니다.
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

			// 재업로드를 위한 타겟 정보를 생성합니다. (UseConfig: false = 설정파일 대신 제공된 배열 사용)
			newTargets := UploadTargets{
				Remotes:   validRemotes,
				UseConfig: false,
			}

			fmt.Printf("[3. Upload] Starting Dis_Upload for %s to %d targets...\n", fileId, len(validRemotes))

			// 남은 리모트들을 대상으로 파일을 분산 저장(업로드)합니다.
			err := Dis_Upload(
				[]string{localTempFilePath, remotePath, fileId},
				newTargets,
				false,
				RoundRobinFromSelectedRemotes,
			)

			// 업로드 결과 처리 및 임시 파일 삭제
			if err != nil {
				fmt.Printf("   ❌ [Error] Upload failed for %s: %v\n", fileId, err)
			} else {
				// 4. 업로드 성공 시 로컬에 저장했던 복구용 임시 파일을 삭제(Cleanup)합니다.
				if err := os.Remove(localTempFilePath); err != nil {
					fmt.Printf("   ⚠️ [Warning] Failed to delete temp file %s: %v\n", localTempFilePath, err)
				}
				fmt.Printf("   ✅ [Migration] Completed for %s\n", fileId)
			}
		}
	}

	// ---------------------------------------------------------
	// [Step 2] Safe Files 처리 (메타데이터 직접 수정)
	// ---------------------------------------------------------
	// 복구 가능한 파일들은 재업로드 없이 메타데이터(json) 상에서 해당 리모트 정보만 제거합니다.
	if len(safeFiles) > 0 {
		// 앞선 Unsafe Files 처리 과정(Dis_rm, Dis_Upload)에서 filesMap이 변경되었을 수 있으므로
		// 최신 상태의 데이터맵을 다시 읽어옵니다.
		if len(unsafeFiles) > 0 {
			filesMap, err = readJsonFile()
			if err != nil {
				return fmt.Errorf("failed to reload datamap: %w", err)
			}
		}

		dirty := false // 데이터맵에 수정사항이 발생했는지 추적하는 플래그
		for _, fileId := range safeFiles {
			fileInfo, exists := filesMap[fileId]
			if !exists {
				continue // 파일이 존재하지 않으면 건너뜀
			}

			// 2-1. 해당 파일의 파편 정보(DistributedFileInfos)에서 삭제될 리모트의 파편 정보를 제거합니다.
			for key, dFile := range fileInfo.DistributedFileInfos {
				if dFile.Remote.Name == remoteName {
					delete(fileInfo.DistributedFileInfos, key)
				}
			}

			// 2-2. 파일 정보의 메인 RemoteList 문자열 배열에서 삭제될 리모트 이름을 제외하고 갱신합니다.
			newRemoteList := []string{}
			for _, rName := range fileInfo.RemoteList {
				if rName != remoteName {
					newRemoteList = append(newRemoteList, rName)
				}
			}
			fileInfo.RemoteList = newRemoteList

			// 2-3. 살아남은 파편들의 RemotePool 배열에서도 삭제된 리모트 객체를 제거합니다.
			for key, dFile := range fileInfo.DistributedFileInfos {
				var newPool []config.Remote
				for _, r := range dFile.RemotePool {
					if r.Name != remoteName {
						newPool = append(newPool, r) // 삭제될 리모트가 아닌 경우만 새 풀에 추가
					}
				}
				dFile.RemotePool = newPool
				// Go 언어의 특징 상 맵(map)의 값인 구조체(Struct)는 복사본이므로 다시 할당해야 값이 변경됩니다.
				fileInfo.DistributedFileInfos[key] = dFile
			}

			// 수정된 파일 정보를 다시 전체 데이터맵에 저장하고 수정됨(dirty) 플래그를 켭니다.
			filesMap[fileId] = fileInfo
			dirty = true
			fmt.Printf("[Update] Metadata updated for '%s' (Cleaned RemotePool)\n", fileInfo.FileName)
		}

		// 수정사항이 있다면 데이터맵 파일(datamap.json)을 디스크에 덮어씁니다.
		if dirty {
			if err := writeJsonFile(getJsonFilePath(), filesMap); err != nil {
				return fmt.Errorf("failed to save updated datamap: %w", err)
			}
		}
	}

	fmt.Println("\nRemote deletion and migration completed successfully.")
	return nil // 에러 없이 성공적으로 함수 종료
}

// checking to see if it terminated abnormally and if so, returning what command is was previously
func CheckFlagAndState() (bool, string, string) {
	filesMap, err := readJsonFile()
	if err != nil {
		fmt.Printf("failed to read json file at checkflag func")
	}

	for fileId, info := range filesMap {
		if info.Flag {
			return info.Flag, info.State, fileId
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

// BatchUpdateDistributedFiles updates multiple shards for a single file in one File I/O operation
func BatchUpdateDistributedFiles(fileId string, updates []DistributedFile) error {
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

	for _, updatedDFile := range updates {
		fileName := updatedDFile.DistributedFile
		if _, ok := fileInfo.DistributedFileInfos[fileName]; ok {
			// Update flags and remote info
			dFile := fileInfo.DistributedFileInfos[fileName]
			dFile.Check = true
			dFile.Remote = updatedDFile.Remote
			fileInfo.DistributedFileInfos[fileName] = dFile
		}
	}

	filesMap[fileId] = fileInfo

	err = writeJsonFile(getJsonFilePath(), filesMap)
	if err != nil {
		return fmt.Errorf("failed to write updated JSON: %v", err)
	}

	return nil
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
