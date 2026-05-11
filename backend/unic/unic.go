package unic

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/backend/unic/common"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/dis_operations"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/walk"
)

var entrytable_path string
var rclone_path string

// Register with Fs
func init() {
	homeDir, _ := os.UserHomeDir()
	entrytable_path = filepath.Join(filepath.Dir(config.GetConfigPath()), "entrytable.jsonl")
	rclone_path = filepath.Join(homeDir, ".config", "rclone", "rclone.conf")
	fs.Register(&fs.RegInfo{
		Name:        "unic",
		Description: "Unified Namespace of Integrated Cloudstorage",
		NewFs:       NewFs,
		MetadataInfo: &fs.MetadataInfo{
			Help: `Any metadata supported by the underlying remote is read and written.`,
		},
		Options: []fs.Option{{
			Name:     "upstreams",
			Help:     "List of space separated upstreams.\n\nCan be 'upstreama:test/dir upstreamb:', '\"upstreama:test/space:ro dir\" upstreamb:', etc.",
			Required: true,
		}, {
			Name:    "cache_time",
			Help:    "Cache time of usage and free space (in seconds).\n\nThis option is only useful when a path preserving policy is used.",
			Default: 120,
		}, {
			Name: "userid",
			Help: "User ID for unic backend",
		}},
	})
}

type Fs struct {
	name     string         // name of this remote
	features *fs.Features   // optional features
	opt      common.Options // parsed options
	root     string         // the path we are working on
	hashSet  hash.Set       // intersection of hash types
	mu       sync.Mutex     // global mutex for metadata operations
}

// Will definitely have info but maybe not meta
type Object struct {
	fs      *Fs       // what this object is part of
	id      string    // ID of the object
	remote  string    // The remote path
	size    int64     // size of the object
	modTime time.Time // modification time of the object
}

// Directory describes a OneDrive directory
type Directory struct {
	fs      *Fs       // what this object is part of
	id      string    // dir ID
	remote  string    // The remote path
	size    int64     // size of directory and contents or -1 if unknown
	items   int64     // number of objects or -1 for unknown
	modTime time.Time // modification time of the object
}

type NodeType string

const (
	NodeTypeFile NodeType = "file"
	NodeTypeDir  NodeType = "dir"
)

type NodeEntry struct {
	Id   string   `json:"id"`
	Name string   `json:"name"`
	Path string   `json:"path"`
	Type NodeType `json:"type"`

	Size    int64     `json:"size"`    // size of the object
	ModTime time.Time `json:"modtime"` // modification time of the object
	Items   int64     `json:"items"`   // number of objects or -1 for unknown
}

func (t NodeType) Valid() bool {
	return t == NodeTypeFile || t == NodeTypeDir
}

func (i *NodeEntry) IsDir() bool  { return i.Type == NodeTypeDir }
func (i *NodeEntry) IsFile() bool { return i.Type == NodeTypeFile }

func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	name = strings.Split(name, "{")[0]
	opt := new(common.Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}

	// Trim root
	root = strings.Trim(root, "/")

	// Make Fs object
	f := &Fs{
		name:     name,
		root:     root,
		opt:      *opt,
		features: &fs.Features{},
	}
	// features 정의
	var features = (&fs.Features{
		CaseInsensitive:          false, // has case insensitive files
		DuplicateFiles:           false, // 동일한 경로, 이름을 가진 파일이 존재할 수 있는지
		ReadMimeType:             false, // can read the mime type of objects
		WriteMimeType:            false, // can set the mime type of objects
		CanHaveEmptyDirectories:  true,  // can have empty directories
		BucketBased:              false, // cloud의 저장소 안에 bucket이라는 최상위 저장소가 존재하는지 여부
		BucketBasedRootOK:        false, // bucket이 있을 시 root에 대한 요청을 허가할 것인지 아닌지 여부
		SetTier:                  false, // 엑세스 빈도에 따라 파일의 등급을 나누는 기능이 있는지
		GetTier:                  false, // 엑세스 빈도에 따라 파일의 등급을 나누는 기능이 있는지
		ServerSideAcrossConfigs:  false, // local을 거치지 않고 server끼리의 파일 복사가 가능한지 여부
		IsLocal:                  false, // is the local backend
		SlowModTime:              true,  // modetime을 확인하는데 시간이 많이 드는지
		SlowHash:                 true,  // hash를 확인하는데 시간이 많이 드는지
		ReadMetadata:             false, // can read metadata from objects, Object.Metadata() 구현 필요, 일단은 false
		WriteMetadata:            false, // can write metadata to objects, 일단은 false
		UserMetadata:             false, // user가 정의한 메타데이터 정의 가능 여부
		ReadDirMetadata:          false, // can read metadata from directories (implements Directory.Metadata), 일단은 false
		WriteDirMetadata:         false, // can write metadata to directories (implements Directory.SetMetadata), 일단은 false
		WriteDirSetModTime:       false, // can write metadata to directories (implements Directory.SetModTime), 일단은 false
		UserDirMetadata:          false, // user가 정의한 메타데이터 정의 가능 여부
		DirModTimeUpdatesOnWrite: false, // indicate writing files to a directory updates its modtime, 일단은 false
		FilterAware:              false, // 파일 필터링 기능을 서버에서 지원해서 그 기능을 사용할 것인지
		PartialUploads:           false, // 업로드중인 파일이 서버상에서 미완성된 상태로 다른 사용자에게 보여주는지 여부
		NoMultiThreading:         true,  // set if can't have multiplethreads on one download open
		Overlay:                  true,  // this wraps one or more backends to add functionality
		ChunkWriterDoesntSeek:    true,  // 대용량 파일을 업로드할 시 rclone은 chunkwriter를 이용해서 파일을 업로드하는데 chunkwriter가 업로드 도중 끊기면 해당 부분을 seek해야할 수도 있음. 그걸 가능하게 할지말지 설정.
	}).Fill(ctx, f)

	// Fs 객체에 features 저장
	f.features = features

	// cache 초기화
	err = f.ClearOSDefaultDownloadDir()
	if err != nil {
		return nil, err
	}

	err = f.MakeOSDefaultDownloadDir()
	if err != nil {
		return nil, err
	}

	return f, nil
}

// Features returns the optional features of this Fs
func (f *Fs) Features() *fs.Features {
	return f.features
}

func (f *Fs) newObject(ctx context.Context, remote string, node *NodeEntry) (fs.Object, error) {
	remote = strings.TrimPrefix(remote, "/")
	o := &Object{
		fs:     f,
		remote: remote,
	}
	var err error
	if node != nil {
		o.id = node.Id
		o.size = node.Size
		o.modTime = node.ModTime
	} else {
		foundNode, err := f.findNodeFromTable(remote)
		if err != nil {
			return nil, err
		}

		o.id = foundNode.Id
		o.size = foundNode.Size
		o.modTime = foundNode.ModTime
	}
	return o, err
}

func (f *Fs) NewObject(ctx context.Context, remote string) (entry fs.Object, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.newObject(ctx, remote, nil)
}

func (f *Fs) GetUserId() string {
	return f.opt.UserID
}

func (f *Fs) findNodeFromTable(remote string) (*NodeEntry, error) {
	remote = strings.TrimPrefix(remote, "/")
	entryTable, err := os.Open(entrytable_path)
	if err != nil {
		return nil, err
	}
	defer entryTable.Close()

	scanner := bufio.NewScanner(entryTable)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var node NodeEntry
		if err := json.Unmarshal(line, &node); err != nil {
			continue
		}
		if strings.TrimPrefix(node.Path, "/") == remote {
			return &node, nil
		}
	}
	return nil, fs.ErrorObjectNotFound
}

func (f *Fs) newDir(node NodeEntry) (entry fs.Directory, err error) {
	d := &Directory{
		fs:      f,
		id:      node.Id,
		remote:  node.Path,
		size:    -1,
		items:   -1,
		modTime: node.ModTime,
	}
	return d, nil
}

// path가 prefix 하위에 있는지 확인
func isUnderDir(path, prefix string) bool {
	if prefix == "" || prefix == "/" {
		return true
	}

	prefix = strings.TrimSuffix(prefix, "/")

	if path == prefix {
		return false
	}

	return strings.HasPrefix(path, prefix+"/")
}

//// path가 prefix의 바로 하위에 있는지 확인
//func isDirectChild(prefix, path_ string) bool {
//	if prefix == "/" {
//		return path.Dir(path_) == "/"
//	}
//	return path.Dir(path_) == prefix
//}

// path가 prefix의 바로 하위에 있는지 확인
func isDirectChild(path_, prefix string) bool {
	// 자기 자신은 제외
	if path_ == prefix || (prefix == "" && path_ == "/") || (prefix == "/" && path_ == "") {
		return false
	}

	parent := path.Dir(path_)

	// 루트 경로 처리 (prefix가 "" 또는 "/"인 경우)
	if prefix == "" || prefix == "/" {
		return parent == "." || parent == "/"
	}
	return parent == prefix
}

func openEntryTable(path string) (*io.PipeReader, error) {
	pr, pw := io.Pipe()

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	go func() {
		defer f.Close()
		defer pw.Close()

		_, err := io.Copy(pw, f)
		if err != nil {
			_ = pw.CloseWithError(err)
		}
	}()

	return pr, nil
}

// entrytable에서 DirEntry 목록 가져오기
func (f *Fs) getList(ctx context.Context, dir string, checkDir func(path, prefix string) bool) ([]fs.DirEntry, error) {
	entryTable, err := os.Open(entrytable_path)
	if err != nil {
		return nil, err
	}
	defer entryTable.Close()

	decoder := json.NewDecoder(entryTable)
	var result []fs.DirEntry

	for {
		var node NodeEntry
		var entry fs.DirEntry

		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		if !checkDir(node.Path, dir) {
			continue
		}

		switch node.Type {
		case "file":
			o, err := f.newObject(ctx, makeRemote(node.Path, f.Root()), &node)
			if err != nil {
				return nil, err
			}
			entry = o
		case "dir":
			d, err := f.newDir(node)
			if err != nil {
				return nil, err
			}
			entry = d
		default:
			return nil, fmt.Errorf("invalid node type: %s", node.Type)
		}

		result = append(result, entry)
	}

	return result, nil
}

// 절대경로를 상대경로로 바꿔줌
// path에서 prefix 제거
func makeRemote(path string, prefix string) string {
	// root 기준이면 그대로 상대경로
	if prefix == "" || prefix == "/" {
		return strings.TrimPrefix(path, "/")
	}

	// root 끝의 '/' 제거
	prefix = strings.TrimSuffix(prefix, "/")

	// p가 root 자체이면 remote는 빈 문자열 (dir entry용)
	if path == prefix {
		return ""
	}

	// root 하위 경로만 허용
	prefix_ := prefix + "/"
	if !strings.HasPrefix(path, prefix_) {
		// root 밖의 경로 → List/ListR 대상 아님
		return ""
	}

	// root 기준 상대 경로로 변환
	return strings.TrimPrefix(path, prefix_)
}

func (f *Fs) ListR(ctx context.Context, dir string, callback fs.ListRCallback) (err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := walk.NewListRHelper(callback)

	entries, err := f.getList(ctx, dir, isUnderDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		list.Add(entry)
	}

	return list.Flush()
}

/* Fs */
// Fs
func (f *Fs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, err := f.getList(ctx, dir, isDirectChild)
	if err != nil {
		return nil, err
	}

	return fs.DirEntries(entries), nil
}

// rclone에 등록된 remote중 unic에 등록된 remote의 배열을 가져오는 method
// seen을 굳이 map[string]struct{}로 해야하나? string 배열을 사용하면 안되나?
func (f *Fs) getUpstreamRemotes() []config.Remote {
	remotes := config.GetRemotes()
	fmt.Printf("\n[DEBUG getUpstreamRemotes] 전체 리모트(config.GetRemotes()) 개수: %d\n", len(remotes))
	for _, r := range remotes {
		fmt.Printf(" - 등록된 리모트: %s (Type: %s)\n", r.Name, r.Type)
	}

	// 동적으로 rclone.conf에서 최신 upstreams 값을 읽어옴 (f.name은 rclone 설정 파일의 섹션 이름)
	var currentUpstreams fs.SpaceSepList
	sectionName := strings.TrimSuffix(f.name, ":")
	if upstreamsStr, found := config.FileGetValue(sectionName, "upstreams"); found {
		_ = currentUpstreams.Set(upstreamsStr)
	} else {
		// 파일을 못 찾거나 키가 없으면 기존 초기화 시 캐싱된 값을 사용
		currentUpstreams = f.opt.Upstreams
	}

	seen := make(map[string]struct{})
	fmt.Printf("[DEBUG getUpstreamRemotes] 파싱된 Upstreams (총 %d개):\n", len(currentUpstreams))
	for _, upstream := range currentUpstreams {
		_, configName, _, _, _ := fs.ParseRemote(upstream)
		//name := strings.TrimSuffix(fsName, ":")
		seen[configName] = struct{}{}
		fmt.Printf(" - Upstream 파싱됨: 원본='%s' -> configName='%s'\n", upstream, configName)
	}

	var result []config.Remote
	fmt.Printf("[DEBUG getUpstreamRemotes] 필터링 시작:\n")
	for _, remote := range remotes {
		if _, ok := seen[remote.Name]; ok {
			result = append(result, remote)
			fmt.Printf(" - [Match] 리모트 '%s' (타입: %s) 추가됨\n", remote.Name, remote.Type)
		} else {
			fmt.Printf(" - [Skip] 리모트 '%s' (타입: %s) 제외됨\n", remote.Name, remote.Type)
		}
	}
	fmt.Printf("[DEBUG getUpstreamRemotes] 최종 반환 리모트 개수: %d\n\n", len(result))

	return result
}

// UNIC을 마운팅한 mount point에 write system call이 들어올 때 실행
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	remote := src.Remote()
	srcfile := in.(*os.File)

	f.mu.Lock()
	defer f.mu.Unlock()
	fmt.Printf("%s [%s] UNIC: Put_: Put_ method Start\n", time.Now().Format("15:04:05.000"), remote)

	// src 파일 핸들로부터 파일 정보(FileInfo) 가져오기
	info, err := srcfile.Stat()
	if err != nil {
		return nil, fmt.Errorf("UNIC: Put_: failed to get file info: %w", err)
	}

	// src가 실존하는 파일인지 확인
	if _, err := os.Stat(srcfile.Name()); err != nil {
		fmt.Printf("UNIC: Put_: File do not exist error: %s\n", err)
	}

	// Call Dis_Upload
	var fileID string
	node, findErr := f.findNodeFromTable(remote)
	if findErr == nil && node != nil {
		// 해당 파일이 원래 존재하던 파일을 단순 덮어쓰기하는 경우 (Rename 없이), 기존 ID를 재사용하여 과거 찌꺼기를 삭제!
		fileID = node.Id
	} else {
		// 새롭게 생성되는 파일(또는 Rename 된 이후 새로 쓰이는 임시/원본 파일)의 경우 ID 충돌이 나지 않도록 유니크한 조합 사용
		fileID = generateHash(remote)
		// 완전히 새로운 파일이 생성될 때만 부모 디렉토리의 수정시간 갱신
		_ = updateParentDirModTime(remote, time.Now())
	}

	fmt.Printf("[%s] UNIC: Put_: dis_upload start\n", remote)
	err = dis_operations.Dis_Upload([]string{srcfile.Name(), remote, fileID}, dis_operations.UploadTargets{Remotes: f.getUpstreamRemotes(), UseConfig: false}, false, dis_operations.RoundRobinFromSelectedRemotes)
	if err != nil {
		return nil, fmt.Errorf("UNIC: Put_: Dis_Upload failed: %w", err)
	}
	fmt.Printf("[%s] UNIC: Put_: dis_upload end\n", remote)

	// 새로운 파일 정보 생성
	newNode := NodeEntry{
		Id:      fileID,
		Name:    filepath.Base(remote),
		Path:    remote,
		Type:    NodeTypeFile,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		Items:   0,
	}

	// dis_upload 성공 시 entrytable.jsonl 파일 갱신 (Atomic Update)
	err = upsertNodeInTable(newNode)
	if err != nil {
		return nil, fmt.Errorf("UNIC: Put: failed to update entrytable: %w", err)
	}

	fmt.Printf("[%s] UNIC: Put: entrytable update success\n", remote)

	// Return the object
	// We construct the Object based on the source info as entry table lookup might fail or be delayed.
	return &Object{
		fs:      f,
		remote:  remote,
		size:    info.Size(),
		modTime: info.ModTime(),
		id:      fileID,
	}, nil
}

func generateHash(remotePath string) string {
	// 파일 이름 기반의 해시만 사용할 경우 Rename으로 인한 ID(Hash) 공유/충돌 버그가 발생하므로 시간 값을 Salt로 가미
	salt := fmt.Sprintf("%d", time.Now().UnixNano())
	backendRemoteHash := sha256.Sum256([]byte(remotePath + salt))
	fileID := hex.EncodeToString(backendRemoteHash[:])

	return fileID
}

// 부모 디렉토리의 수정시간을 갱신하는 함수
func updateParentDirModTime(childPath string, t time.Time) error {
	childPath = strings.TrimPrefix(childPath, "/")
	parentPath := path.Dir(childPath)
	if parentPath == "" || parentPath == "." || parentPath == "/" {
		return nil
	}

	oldFile, err := os.Open(entrytable_path)
	if err != nil {
		return err
	}
	defer oldFile.Close()

	tempPath := entrytable_path + ".tmp." + fmt.Sprintf("%d", time.Now().UnixNano())
	newFile, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	defer newFile.Close()

	scanner := bufio.NewScanner(oldFile)
	encoder := json.NewEncoder(newFile)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var node NodeEntry
		if err := json.Unmarshal(line, &node); err != nil {
			continue
		}

		if strings.TrimPrefix(node.Path, "/") == parentPath && node.Type == "dir" {
			node.ModTime = t
		}

		if err := encoder.Encode(node); err != nil {
			_ = os.Remove(tempPath)
			return err
		}
	}

	oldFile.Close()
	newFile.Close()
	return os.Rename(tempPath, entrytable_path)
}

// UNIC을 마운팅한 mount point에 mkdir system call이 들어올 때 실행
func (f *Fs) Mkdir(ctx context.Context, dirPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	fmt.Printf("%s UNIC: Mkdir: Mkdir method Start\n", time.Now().Format("15:04:05.000"))

	// open entrytable
	entryTable, err := os.OpenFile(entrytable_path, os.O_WRONLY|os.O_APPEND, 0755)
	if err != nil {
		return err
	}
	defer entryTable.Close()

	// entrytable에 쓸 NodeEntry 정의
	var node NodeEntry // entrytable에 쓸 데이터를 저장하고 있을 변수

	node.Id = generateHash(dirPath)
	node.Name = filepath.Base(dirPath)
	node.Path = dirPath
	node.Items = 0
	node.Size = 0
	node.Type = "dir"
	node.ModTime = time.Now() // 디렉토리 생성 시간 지정

	// update entrytable
	encoder := json.NewEncoder(entryTable)
	if err := encoder.Encode(node); err != nil {
		return err
	}
	entryTable.Close()

	// 부모 디렉토리의 수정시간 갱신 (mkdir)
	_ = updateParentDirModTime(dirPath, node.ModTime)

	return nil
}

func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	fmt.Printf("%s [%s] UNIC: Rmdir: Rmdir method Start\n", time.Now().Format("15:04:05.000"), dir)

	// open entrytable
	entryTable, err := os.OpenFile(entrytable_path, os.O_RDWR|os.O_APPEND, 0755)
	if err != nil {
		return err
	}
	defer entryTable.Close()

	// entrytable decoder/encoder 생성
	decoder := json.NewDecoder(entryTable)

	// update entrytable
	for {
		// entrytable에서 node 하나씩 가져옴
		var node NodeEntry
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		// node가 dir의 하위 디렉토리인지 확인
		if !isUnderDir(node.Path, dir) {
			continue
		}

		// node가 dir일 경우 entrytable에서 삭제
		// node가 file일 경우는 어차피 rm -rf 명령어로 재귀적으로 삭제되면서
		// 해당 file에 대한 Remove() method가 실행됨
		if node.Type == "dir" {
			removeNodeFromTable(node.Path)
		} else {
			return fmt.Errorf("entrytable.jsonl type error\n")
		}
	}

	// 삭제 완료 후 부모 디렉토리 수정시간 갱신 (rmdir)
	_ = updateParentDirModTime(dir, time.Now())

	return nil
}

func removeNodeFromTable(targetNode string) error {
	targetNode = strings.TrimPrefix(targetNode, "/")
	oldFile, err := os.Open(entrytable_path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer oldFile.Close()

	tempPath := entrytable_path + ".tmp." + fmt.Sprintf("%d", time.Now().UnixNano())
	newFile, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	defer newFile.Close()

	scanner := bufio.NewScanner(oldFile)
	encoder := json.NewEncoder(newFile)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var node NodeEntry
		if err := json.Unmarshal(line, &node); err != nil {
			continue
		}
		if strings.TrimPrefix(node.Path, "/") == targetNode {
			continue
		}
		if err := encoder.Encode(node); err != nil {
			_ = os.Remove(tempPath)
			return err
		}
	}

	oldFile.Close()
	newFile.Close()
	return os.Rename(tempPath, entrytable_path)
}

func renameInDatamap(oldName, newName string) error {
	oldName = strings.TrimPrefix(oldName, "/")
	newName = strings.TrimPrefix(newName, "/")

	datamapPath := filepath.Join(os.Getenv("HOME"), ".config/rclone/data/datamap.json")
	data, err := os.ReadFile(datamapPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var datamap map[string]interface{}
	if err := json.Unmarshal(data, &datamap); err != nil {
		return err
	}

	found := false
	for _, v := range datamap {
		info, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		if info["backend_file_path"] == oldName {
			info["original_file_name"] = newName
			info["backend_file_path"] = newName
			found = true
		}
	}

	if !found {
		return nil // 혹은 필요에 따라 처리
	}

	newData, err := json.MarshalIndent(datamap, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(datamapPath, newData, 0644)
}

func renameNodeInTable(oldPath, newPath string) error {
	oldPath = strings.TrimPrefix(oldPath, "/")
	newPath = strings.TrimPrefix(newPath, "/")

	oldFile, err := os.Open(entrytable_path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer oldFile.Close()

	tempPath := entrytable_path + ".tmp." + fmt.Sprintf("%d", time.Now().UnixNano())
	newFile, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	defer newFile.Close()

	scanner := bufio.NewScanner(oldFile)
	encoder := json.NewEncoder(newFile)
	found := false

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var node NodeEntry
		if err := json.Unmarshal(line, &node); err != nil {
			continue
		}

		if strings.TrimPrefix(node.Path, "/") == oldPath {
			node.Path = newPath
			node.Name = filepath.Base(newPath)
			found = true
		}

		if err := encoder.Encode(node); err != nil {
			_ = os.Remove(tempPath)
			return err
		}
	}

	oldFile.Close()
	newFile.Close()

	if !found {
		os.Remove(tempPath)
		return fs.ErrorObjectNotFound
	}
	return os.Rename(tempPath, entrytable_path)
}

func (f *Fs) Name() string   { return f.name }
func (f *Fs) Root() string   { return f.root }
func (f *Fs) String() string { return f.name }

func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fmt.Printf("%s [%s] UNIC: Move: Move method Start. src: %s, remote: %s\n", time.Now().Format("15:04:05.000"), src.Remote(), src.Remote(), remote)

	srcObj, ok := src.(*Object)
	if !ok {
		return nil, fs.ErrorCantMove
	}

	srcPath := srcObj.remote

	// 1. entrytable.jsonl 및 datamap.json 업데이트
	_ = renameInDatamap(srcPath, remote)
	if err := renameNodeInTable(srcPath, remote); err != nil {
		// 이미 갱신되어 있을 수 있으므로 확인
		if alreadyMovedNode, ferr := f.findNodeFromTable(remote); ferr == nil {
			fmt.Printf("UNIC: Move: Node already at destination %s\n", remote)
			_ = f.RenameLocalCachePhysical(srcPath, remote)
			return f.newObject(ctx, remote, alreadyMovedNode)
		}
		return nil, err
	}

	// 2. 물리적 파일 이름 변경
	_ = f.RenameLocalCachePhysical(srcPath, remote)

	movedNode, err := f.findNodeFromTable(remote)
	if err != nil {
		return nil, err
	}
	return f.newObject(ctx, remote, movedNode)
}

// RenameLocalCache renames the local cache file and the entry in the table
func (f *Fs) RenameLocalCache(oldPath, newPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	_ = f.RenameLocalCachePhysical(oldPath, newPath)
	_ = renameInDatamap(oldPath, newPath)
	_ = renameNodeInTable(oldPath, newPath)
	return nil
}

func (f *Fs) RenameLocalCachePhysical(oldPath, newPath string) error {
	oldPath = strings.TrimPrefix(oldPath, "/")
	newPath = strings.TrimPrefix(newPath, "/")

	oldLocalPath, err1 := f.MakeOSDownloadPath(oldPath)
	newLocalPath, err2 := f.MakeOSDownloadPath(newPath)
	if err1 != nil || err2 != nil {
		return nil
	}

	if _, err := os.Stat(oldLocalPath); err == nil {
		os.MkdirAll(filepath.Dir(newLocalPath), 0755)
		if err := os.Rename(oldLocalPath, newLocalPath); err == nil {
			fmt.Printf("UNIC: renameLocalCachePhysical: successfully renamed %s -> %s\n", oldLocalPath, newLocalPath)
		}
	}
	return nil
}

func (f *Fs) DirMove(ctx context.Context, src fs.Fs, srcRemote, dstRemote string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	fmt.Printf("%s [%s] UNIC: DirMove: DirMove method Start. srcRemote: %s, dstRemote: %s\n", time.Now().Format("15:04:05.000"), srcRemote, srcRemote, dstRemote)

	if src.Name() != f.Name() || src.Root() != f.Root() {
		return fs.ErrorCantDirMove
	}

	oldFile, err := os.OpenFile(entrytable_path, os.O_RDWR, 0755)
	if err != nil {
		return err
	}
	defer oldFile.Close()

	tempPath := entrytable_path + ".tmp." + fmt.Sprintf("%d", time.Now().UnixNano())
	newFile, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	defer newFile.Close()

	scanner := bufio.NewScanner(oldFile)
	encoder := json.NewEncoder(newFile)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var node NodeEntry
		if err := json.Unmarshal(line, &node); err != nil {
			continue
		}

		nodePath := strings.TrimPrefix(node.Path, "/")
		if nodePath == srcRemote || isUnderDir(nodePath, srcRemote) {
			if nodePath == srcRemote {
				node.Path = dstRemote
				node.Name = filepath.Base(dstRemote)
			} else {
				node.Path = dstRemote + "/" + strings.TrimPrefix(nodePath, srcRemote+"/")
				node.Name = filepath.Base(node.Path)
			}
		}

		if err := encoder.Encode(node); err != nil {
			return err
		}
	}

	oldFile.Close()
	newFile.Close()

	if err := os.Rename(tempPath, entrytable_path); err != nil {
		os.Remove(tempPath)
		return err
	}

	// Rename local cache directory
	oldLocalDir, err1 := f.MakeOSDownloadPath(srcRemote)
	newLocalDir, err2 := f.MakeOSDownloadPath(dstRemote)
	if err1 == nil && err2 == nil {
		if _, err := os.Stat(oldLocalDir); err == nil {
			// Ensure the destination directory exists
			os.MkdirAll(filepath.Dir(newLocalDir), 0755)
			if err := os.Rename(oldLocalDir, newLocalDir); err != nil {
				fmt.Printf("UNIC: DirMove: failed to rename local cache dir from %s to %s: %v\n", oldLocalDir, newLocalDir, err)
			} else {
				fmt.Printf("UNIC: DirMove: successfully renamed local cache dir from %s to %s\n", oldLocalDir, newLocalDir)
			}
		}
	}

	return nil
}

func (f *Fs) Precision() time.Duration {
	return time.Nanosecond
}

func (f *Fs) Hashes() hash.Set {
	return hash.Set(hash.None) // 최소 해시 없음
}

/* Object */
// DirEntry
func (o *Object) Fs() fs.Info {
	return o.fs
}

func (o *Object) String() string {
	return o.remote
}

func (o *Object) Remote() string {
	return o.remote
}

func (o *Object) ModTime(ctx context.Context) time.Time {
	return o.modTime
}

func (o *Object) Size() int64 {
	return o.size
}

func (o *Object) Hash(ctx context.Context, ty hash.Type) (string, error) { return "", nil }
func (o *Object) Storable() bool                                         { return true }
func (o *Object) SetModTime(ctx context.Context, t time.Time) error {
	o.fs.mu.Lock()
	defer o.fs.mu.Unlock()

	o.modTime = t

	// entrytable.jsonl 갱신
	oldFile, err := os.Open(entrytable_path)
	if err != nil {
		return err
	}
	defer oldFile.Close()

	tempPath := entrytable_path + ".tmp." + fmt.Sprintf("%d", time.Now().UnixNano())
	newFile, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	defer newFile.Close()

	scanner := bufio.NewScanner(oldFile)
	encoder := json.NewEncoder(newFile)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var node NodeEntry
		if err := json.Unmarshal(line, &node); err != nil {
			continue
		}

		// 해당 파일의 ModTime 갱신
		if strings.TrimPrefix(node.Path, "/") == strings.TrimPrefix(o.remote, "/") && node.Type == "file" {
			node.ModTime = t
		}

		if err := encoder.Encode(node); err != nil {
			_ = os.Remove(tempPath)
			return err
		}
	}

	oldFile.Close()
	newFile.Close()

	return os.Rename(tempPath, entrytable_path)
}

// mount 시 캐시 초기화
func (f *Fs) MakeOSDefaultDownloadDir() error {
	defaultDownloadPath, _ := f.MakeOSDefaultDownloadPath()
	err := os.MkdirAll(defaultDownloadPath, 0755)

	return err
}

// unmount 시 캐시 초기화
func (f *Fs) ClearOSDefaultDownloadDir() error {
	defaultDownloadPath, _ := f.MakeOSDefaultDownloadPath()
	err := os.RemoveAll(defaultDownloadPath)

	return err
}

func (f *Fs) MakeOSDefaultDownloadPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(
		home,
		"rclone",
		"Download",
		f.GetUserId(),
		strings.Split(f.Name(), "{")[0],
	), nil
}

func (f *Fs) MakeOSDownloadPath(remotePath string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(
		home,
		"rclone",
		"Download",
		f.GetUserId(),
		strings.Split(f.Name(), "{")[0],
		remotePath,
	), nil
}

// UNIC을 마운팅한 mount point에 read system call이 들어올 때 실행
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	fmt.Printf("%s [%s] UNIC: Open: Open method Start\n", time.Now().Format("15:04:05.000"), o.Remote())

	// Download 폴더 생성 : home/rclone/Download/{userId}/{remotename}/{remotepath}
	remotePath := o.Remote()
	downloadPath, err := o.fs.MakeOSDownloadPath(remotePath)
	if err != nil {
		return nil, err
	}
	downloadDir := filepath.Dir(downloadPath)
	err = os.MkdirAll(downloadDir, 0755)
	if err != nil {
		return nil, err
	}

	// Dis_Download 로직 호출
	fileId := o.id

	// 파일이 이미 Download 폴더에 존재한다면, Dis_Download 호출하지 않음
	fi, err := os.Stat(downloadPath)
	if fi == nil {
		fmt.Printf("[%s] UNIC: Open: Dis_Download start\n", remotePath)
		err = dis_operations.Dis_Download([]string{fileId, downloadDir, remotePath}, false)
		if err != nil {
			return nil, fmt.Errorf("UNIC: Open: Dis_Download 실패: fileId=%s, error=%v", fileId, err)
		}
		fmt.Printf("[%s] UNIC: Open: Dis_Download end\n", remotePath)
	}

	// Dis_Download 결과 만들어진 파일 open
	f, err := os.Open(downloadPath)
	if err != nil {
		os.Remove(downloadPath)
		return nil, fmt.Errorf("UNIC: Open: downloadedFilePath open 실패: fileId=%s, error=%v", fileId, err)
	}

	return f, nil
}

func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {

	return nil
}

// UNIC을 마운팅한 mount point에 unlink system call이 들어올 때 실행
func (o *Object) Remove(ctx context.Context) error {
	o.fs.mu.Lock()
	defer o.fs.mu.Unlock()
	fmt.Printf("%s UNIC: Remove: Remove method start\n", time.Now().Format("15:04:05.000"))

	// dis_rm 수행
	fmt.Printf("[%s] UNIC: Remove: Remove dis_rm start\n", o.remote)
	fileId := o.id
	err := dis_operations.Dis_rm([]string{fileId}, false)
	fmt.Printf("[%s] UNIC: Remove: Remove dis_rm end\n", o.remote)
	if err != nil {
		return err
	}

	//fmt.Println("UNIC: Remove: Remove Success")

	// dis_rm 수행 후 entrytable.jsonl에서 해당 파일 항목 삭제
	err = removeNodeFromTable(o.remote)
	if err != nil {
		fs.Errorf(o.remote, "UNIC: Remove: failed to remove from entrytable: %v", err)
	}

	// 파일 삭제 후 부모 디렉토리 수정시간 갱신 (unlink)
	_ = updateParentDirModTime(o.remote, time.Now())

	return nil
}

func (d *Directory) Fs() fs.Info {
	return d.fs
}

func (d *Directory) String() string {
	return d.remote
}

func (d *Directory) Remote() string {
	return d.remote
}

func (d *Directory) ModTime(context.Context) time.Time {
	return d.modTime
}

func (d *Directory) Size() int64 {
	return d.size
}

func (d *Directory) Items() int64 {
	return d.items
}

func (d *Directory) ID() string {
	return fmt.Sprintf("%s", d.id)
}

// RemoveLocalCache removes the local cache file and the entry in the table
func (f *Fs) RemoveLocalCache(path string) error {
	path = strings.TrimPrefix(path, "/")
	f.mu.Lock()
	defer f.mu.Unlock()

	localPath, err := f.MakeOSDownloadPath(path)
	if err == nil {
		if _, err := os.Stat(localPath); err == nil {
			_ = os.Remove(localPath)
			fmt.Printf("UNIC: RemoveLocalCache: successfully removed local file %s\n", localPath)
		}
	}

	_ = removeNodeFromTable(path)
	return nil
}

func upsertNodeInTable(newNode NodeEntry) error {
	targetPath := strings.TrimPrefix(newNode.Path, "/")

	// 1. Open the old file to read
	oldFile, err := os.Open(entrytable_path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// 2. Create a temporary file to write
	tempPath := entrytable_path + ".tmp." + fmt.Sprintf("%d", time.Now().UnixNano())
	newFile, err := os.Create(tempPath)
	if err != nil {
		if oldFile != nil {
			oldFile.Close()
		}
		return err
	}
	defer newFile.Close()

	encoder := json.NewEncoder(newFile)

	// 3. One pass update: Copy old entries (except the one being updated)
	if oldFile != nil {
		scanner := bufio.NewScanner(oldFile)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var node NodeEntry
			if err := json.Unmarshal(line, &node); err != nil {
				continue
			}
			// Skip the entry we are about to update
			if strings.TrimPrefix(node.Path, "/") == targetPath {
				continue
			}
			if err := encoder.Encode(node); err != nil {
				oldFile.Close()
				_ = os.Remove(tempPath)
				return err
			}
		}
		oldFile.Close()
	}

	// 4. Append the new entry
	if err := encoder.Encode(newNode); err != nil {
		_ = os.Remove(tempPath)
		return err
	}

	newFile.Close()

	// 5. Atomic Rename (Watcher will 100% detect this)
	return os.Rename(tempPath, entrytable_path)
}
