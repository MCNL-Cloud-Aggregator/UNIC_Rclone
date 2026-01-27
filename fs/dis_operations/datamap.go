package dis_operations

import (
	"bufio" // CheckAndDeleteRemote 함수에서 사용
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings" // CheckAndDeleteRemote 함수에서 사용
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
func MakeDataMap(originalFilePath string, distributedFiles []DistributedFile, disFileSize int64, paddingAmount int64, shard int, parity int) error {
	if originalFilePath == "" {
		return errors.New("originalFilePath cannot be empty")
	}

	jsonFilePath := getJsonFilePath()

	originalFileName := filepath.Base(originalFilePath)
	originalFileInfo, err := os.Stat(originalFilePath)
	if err != nil {
		return fmt.Errorf("failed to stat original file: %v", err)
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
		FileSize:             originalFileInfo.Size(),
		DisFileSize:          disFileSize,
		Shard:                shard,
		Parity:               parity,
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

	FilesMap[originalFileName] = newFileInfo
	return writeJsonFile(jsonFilePath, FilesMap)
}

func RemoveFileFromMetadata(fileName string) error {
	filesMap, err := readJsonFile()
	if err != nil {
		return err
	}

	delete(filesMap, fileName)

	return writeJsonFile(getJsonFilePath(), filesMap)
}

func GetFileInfoStruct(fileName string) (FileInfo, error) {
	filesMap, err := readJsonFile()
	if err != nil {
		return FileInfo{}, err
	}

	if fileInfo, exists := filesMap[fileName]; exists {
		return fileInfo, nil
	}

	return FileInfo{}, fmt.Errorf("file name '%s' not found", fileName)
}

func DoesFileStructExist(fileName string) (bool, error) {
	filesMap, err := readJsonFile()
	if err != nil {
		return false, err
	}

	_, exists := filesMap[fileName]
	return exists, nil
}

func GetDistributedFileStruct(fileName string) ([]DistributedFile, error) {
	filesMap, err := readJsonFile()
	if err != nil {
		return nil, err
	}

	fileInfo, exists := filesMap[fileName]
	if !exists {
		return nil, fmt.Errorf("file name '%s' not found", fileName)
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

// 재업로드해야 할 파일 목록을 임시 저장할 공간
var filesToReupload []string

// 1. 삭제 전 실행 (Pre-Hook)
func CheckAndDeleteRemote(remoteName string) (bool, error) {
	filesToReupload = []string{}

	filesMap, err := readJsonFile()
	if err != nil {
		return false, fmt.Errorf("failed to read datamap: %w", err)
	}

	var soleDependencyFiles []string // 이 Remote에만 있는 파일 (삭제됨)
	var redundancyFiles []string     // 다른 Remote에도 있는 파일 (복구 가능)

	for fileName, fileInfo := range filesMap {
		usedRemotes := make(map[string]bool)
		for _, shard := range fileInfo.DistributedFileInfos {
			usedRemotes[shard.Remote.Name] = true
		}

		// 해당 Remote를 사용하고 있는지 확인
		if usedRemotes[remoteName] {
			// Remote가 1개뿐인데 그게 삭제 대상이라면 -> 유일한 의존성 (삭제 불가피)
			if len(usedRemotes) == 1 {
				soleDependencyFiles = append(soleDependencyFiles, fileName)
			} else {
				// 다른 Remote에도 파편이 있음 -> 복구(마이그레이션) 가능
				redundancyFiles = append(redundancyFiles, fileName)
			}
		}
	}

	totalAffected := len(soleDependencyFiles) + len(redundancyFiles)
	if totalAffected == 0 {
		return true, nil
	}

	// 사용자에게 상황 보고
	fmt.Printf("\n[Check] Remote '%s' affects %d files.\n", remoteName, totalAffected)

	if len(soleDependencyFiles) > 0 {
		fmt.Println(" --- Files to be DELETED (backup remotes): ---")
		for _, f := range soleDependencyFiles {
			fmt.Println("  - " + f)
		}
	}
	if len(redundancyFiles) > 0 {
		fmt.Println(" --- Files to be MIGRATED (Download -> Rm -> Re-Upload): ---")
		for _, f := range redundancyFiles {
			fmt.Println("  - " + f)
		}
	}

	fmt.Print("\nProceed with this operation? (y/N): ")
	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))

	if response != "y" && response != "yes" {
		return false, nil
	}

	// --- 작업 시작 ---

	tempDir := filepath.Join(os.TempDir(), "unic_migration")
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return false, fmt.Errorf("failed to create temp dir: %w", err)
	}

	// A. 복구 가능한 파일들 처리 (Download -> Rm)
	for _, fileName := range redundancyFiles {
		fmt.Printf("[Migrating] Processing %s...\n", fileName)

		// 1. 다운로드 (복원)
		fmt.Printf(" -> Downloading %s for backup...\n", fileName)
		err := Dis_Download([]string{fileName, tempDir}, false)
		if err != nil {
			fmt.Printf("   [Error] Download failed for %s. Skipping migration: %v\n", fileName, err)
			continue // 다운로드 실패하면 삭제도 하지 않음 (보존)
		}

		// 2. 메타데이터 및 파편 삭제 (Dis_rm)
		fmt.Printf(" -> Removing old metadata/shards for %s...\n", fileName)
		err = Dis_rm([]string{fileName}, false)
		if err != nil {
			return false, fmt.Errorf("failed Dis_rm during migration of %s: %w", fileName, err)
		}

		// 3. 재업로드 목록에 추가 (나중에 Post-Hook에서 처리)
		fullPath := filepath.Join(tempDir, fileName)
		filesToReupload = append(filesToReupload, fullPath)
	}

	homeDir, _ := os.UserHomeDir()
	backupDir := filepath.Join(homeDir, ".config", "rclone", "backup")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		fmt.Printf("Error creating backup dir: %v\n", err)
		return false, err
	}
	fmt.Printf("Backup directory is ready at: %s\n", backupDir)
	// B. 유일한 의존성 파일들 처리 (Backup 후 Rm)
	for _, fileName := range soleDependencyFiles {
		fmt.Printf(" -> Downloading %s for backup...\n", fileName)
		err := Dis_Download([]string{fileName, backupDir}, false)
		if err != nil {
			fmt.Printf("   [Error] Download failed for %s. Skipping migration: %v\n", fileName, err)
			continue // 다운로드 실패하면 삭제도 하지 않음 (보존)
		}

		fmt.Printf("[Deleting] Removing %s (Sole dependency)...\n", fileName)
		err = Dis_rm([]string{fileName}, false)
		if err != nil {
			fmt.Printf("   [Error] Failed to delete %s: %v\n", fileName, err)
		}
	}

	fmt.Println("Pre-delete actions completed. Proceeding to delete remote config.")
	return true, nil
}

// 2. 삭제 후 실행 (Post-Hook)
func ReuploadMigratedFiles(remoteName string) error {
	if len(filesToReupload) == 0 {
		return nil
	}

	fmt.Printf("\n[Post-Action] Re-uploading %d migrated files...\n", len(filesToReupload))

	lb := ResourceBased

	for _, f := range filesToReupload {
		err := Dis_Upload([]string{f}, UploadTargets{UseConfig: true}, false, lb)
		if err != nil {
			return fmt.Errorf("failed to re-upload files: %w", err)
		}
	}

	fmt.Println("All migrated files have been re-uploaded successfully.")

	for _, fullPath := range filesToReupload {
		err := os.Remove(fullPath)
		if err != nil {
			fmt.Printf("Warning: Failed to delete temp file %s: %v\n", fullPath, err)
		}
	}

	if len(filesToReupload) > 0 {
		// 파일 경로: /var/.../unic_migration/file.jpg -> 폴더 경로: /var/.../unic_migration
		dirPath := filepath.Dir(filesToReupload[0])

		// 폴더 삭제 시도 (파일을 다 지웠으니 비어있어야 함)
		err := os.Remove(dirPath)
		if err == nil {
			fmt.Printf("Removed temporary directory: %s\n", dirPath)
		} else {
			// 만약 시스템 파일(.DS_Store 등)이 남아있어 안 지워질 수도 있음 -> RemoveAll로 강제 삭제도 가능
			// os.RemoveAll(dirPath) // 강제 삭제를 원하면 이걸 쓰세요
			fmt.Printf("Warning: Could not remove temp dir (might not be empty): %s\n", dirPath)
		}
	}

	// 대기열 초기화
	filesToReupload = []string{}
	return nil
}

// Updating file flag to true.
// this function is used when downloading or deleting a file.
func UpdateFileFlag(originalFileName string, state string) error {
	jsonFileMutex.Lock()
	defer jsonFileMutex.Unlock()

	filesMap, err := readJsonFile()
	if err != nil {
		return fmt.Errorf("failed to read JSON file: %v", err)
	}

	fileInfo, exists := filesMap[originalFileName]
	if !exists {
		return fmt.Errorf("file '%s' not found\n", originalFileName)
	}

	fileInfo.Flag = true
	fileInfo.State = state
	filesMap[originalFileName] = fileInfo

	if err := writeJsonFile(getJsonFilePath(), filesMap); err != nil {
		return fmt.Errorf("failed to write updated JSON: %v", err)
	}

	return nil
}

// updating distributedfile check flag after uploading, downloading or removing
func updateDistributedFile(originalFileName, distributedFileName string, updateFunc func(*DistributedFile) error) error {
	jsonFileMutex.Lock()
	defer jsonFileMutex.Unlock()

	filesMap, err := readJsonFile()
	if err != nil {
		return fmt.Errorf("failed to read JSON file: %v", err)
	}

	fileInfo, exists := filesMap[originalFileName]
	if !exists {
		return fmt.Errorf("file '%s' not found", originalFileName)
	}

	dFile, exists := fileInfo.DistributedFileInfos[distributedFileName]
	if !exists {
		return fmt.Errorf("distributed file '%s' not found for original file '%s'", distributedFileName, originalFileName)
	}

	// Apply the update function
	if err := updateFunc(&dFile); err != nil {
		return err
	}

	fileInfo.DistributedFileInfos[distributedFileName] = dFile
	filesMap[originalFileName] = fileInfo

	err = writeJsonFile(getJsonFilePath(), filesMap)
	if err != nil {
		return fmt.Errorf("failed to write updated JSON: %v", err)
	}

	//fmt.Println("Check flag updated!")
	return nil
}

func UpdateDistributedFile_CheckFlag(originalFileName, distributedFileName string, newCheck bool) error {
	return updateDistributedFile(originalFileName, distributedFileName, func(dFile *DistributedFile) error {
		dFile.Check = newCheck
		return nil
	})
}

func UpdateDistributedFile_CheckFlagAndRemote(originalFileName, distributedFileName string, newCheck bool, remote Remote) error {
	return updateDistributedFile(originalFileName, distributedFileName, func(dFile *DistributedFile) error {
		dFile.Check = newCheck
		dFile.Remote = remote
		return nil
	})
}

// resetting file check flag after finishing operation
func ResetCheckFlag(originalFileName string) error {
	jsonFileMutex.Lock()
	defer jsonFileMutex.Unlock()

	filesMap, err := readJsonFile()
	if err != nil {
		return fmt.Errorf("failed to read JSON file: %v", err)
	}

	fileInfo, exists := filesMap[originalFileName]
	if !exists {
		return fmt.Errorf("failed to reset flag: original file '%s' not found", originalFileName)
	}

	fileInfo.Flag = false

	for key, dFile := range fileInfo.DistributedFileInfos {
		dFile.Check = false
		fileInfo.DistributedFileInfos[key] = dFile
	}

	filesMap[originalFileName] = fileInfo

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
func GetUncompletedFileInfo(originalFileName string) ([]DistributedFile, error) {
	filesMap, err := readJsonFile()
	if err != nil {
		return nil, fmt.Errorf("failed to read JSON file: %v", err)
	}

	fileInfo, exists := filesMap[originalFileName]
	if !exists {
		return nil, fmt.Errorf("original file '%s' not found", originalFileName)
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
