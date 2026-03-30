package unic

import (
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

func (f *Fs) newObject(ctx context.Context, remote string, node *NodeEntry) (fs.Object, error) {
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
	return f.newObject(ctx, remote, nil)
}

func (f *Fs) GetUserId() string {
	return f.opt.UserID
}

func (f *Fs) findNodeFromTable(remote string) (*NodeEntry, error) {
	entryTable, err := os.Open(entrytable_path)
	if err != nil {
		return nil, err
	}
	defer entryTable.Close()

	decoder := json.NewDecoder(entryTable)
	for {
		var node NodeEntry
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		if node.Path == remote {
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

	seen := make(map[string]struct{})
	for _, upstream := range f.opt.Upstreams {
		_, configName, _, _, _ := fs.ParseRemote(upstream)
		//name := strings.TrimSuffix(fsName, ":")
		seen[configName] = struct{}{}
	}

	var result []config.Remote
	for _, remote := range remotes {
		if _, ok := seen[remote.Name]; ok {
			result = append(result, remote)
		}
	}

	return result
}

// UNIC을 마운팅한 mount point에 write system call이 들어올 때 실행
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	//fmt.Printf("%s UNIC: Put: Put method Start\n", time.Now().Format("15:04:05.000"))

	//// Create a temporary directory
	////fmt.Println("UNIC: Put: Create a temporary directory start")
	//tempDir, err := os.MkdirTemp("", "unic_upload")
	//if err != nil {
	//	return nil, fmt.Errorf("UNIC: Put: failed to create temp dir: %w", err)
	//}
	//defer os.RemoveAll(tempDir) // Clean up tempDir

	//// Create the file with the correct name
	////fmt.Println("UNIC: Put: Create a temporary file start")
	//tempFilePath := filepath.Join(tempDir, filepath.Base(src.Remote()))
	////fmt.Printf("src.Remote(): %s, tempFilePath: %s\n", src.Remote(), tempFilePath)
	//tempFile, err := os.Create(tempFilePath)
	//if err != nil {
	//	return nil, fmt.Errorf("UNIC: Put: failed to create temp file: %w", err)
	//}

	//// Copy content
	////fmt.Println("UNIC: Put: Copy content to temp file start")
	//_, err = io.Copy(tempFile, in)
	//if err != nil {
	//	return nil, fmt.Errorf("UNIC: Put: failed to write to temp file: %w", err)
	//}

	//tempFile.Sync() // 필수적인가?
	//closeErr := tempFile.Close()
	//if closeErr != nil {
	//	return nil, fmt.Errorf("UNIC: Put: failed to close temp file: %w", closeErr)
	//}

	//// Call Dis_Upload
	////fmt.Println("UNIC: Put: dis_upload start")
	////fmt.Printf("tempFilePath: %s, remotes: %s\n", tempFilePath, f.getUpstreamRemotes())

	//remotePath := src.Remote()
	//fileID := generateHash((remotePath))
	//err = dis_operations.Dis_Upload([]string{tempFilePath, remotePath, fileID}, dis_operations.UploadTargets{Remotes: f.getUpstreamRemotes(), UseConfig: false}, false, dis_operations.RoundRobinFromSelectedRemotes)
	//if err != nil {
	//	return nil, fmt.Errorf("UNIC: Put: Dis_Upload failed: %w", err)
	//}
	////fmt.Println("UNIC: Put: Put Success")

	//// Return the object
	//// We construct the Object based on the source info as entry table lookup might fail or be delayed.
	return &Object{
		fs:      f,
		remote:  src.Remote(),
		size:    src.Size(),
		modTime: src.ModTime(ctx),
	}, nil
}

// UNIC을 마운팅한 mount point에 write system call이 들어올 때 실행
func (f *Fs) Put_(ctx context.Context, src *os.File, remote string, options ...fs.OpenOption) (fs.Object, error) {
	fmt.Printf("%s [%s] UNIC: Put_: Put_ method Start\n", time.Now().Format("15:04:05.000"), remote)

	// src 파일 핸들로부터 파일 정보(FileInfo) 가져오기
	info, err := src.Stat()
	if err != nil {
		return nil, fmt.Errorf("UNIC: Put_: failed to get file info: %w", err)
	}

	// src가 실존하는 파일인지 확인
	if _, err := os.Stat(src.Name()); err != nil {
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
	err = dis_operations.Dis_Upload([]string{src.Name(), remote, fileID}, dis_operations.UploadTargets{Remotes: f.getUpstreamRemotes(), UseConfig: false}, false, dis_operations.RoundRobinFromSelectedRemotes)
	if err != nil {
		return nil, fmt.Errorf("UNIC: Put_: Dis_Upload failed: %w", err)
	}
	fmt.Printf("[%s] UNIC: Put_: dis_upload end\n", remote)

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
	parentPath := path.Dir(childPath)
	if parentPath == "" || parentPath == "." || parentPath == "/" {
		return nil
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

	decoder := json.NewDecoder(oldFile)
	encoder := json.NewEncoder(newFile)

	for {
		var node NodeEntry
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		if node.Path == parentPath && node.Type == "dir" {
			node.ModTime = t
		}

		if err := encoder.Encode(node); err != nil {
			return err
		}
	}

	oldFile.Close()
	newFile.Close()

	return os.Rename(tempPath, entrytable_path)
}

// UNIC을 마운팅한 mount point에 mkdir system call이 들어올 때 실행
func (f *Fs) Mkdir(ctx context.Context, dirPath string) error {
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
	if fi, err := entryTable.Stat(); err == nil && fi.Size() > 0 {
		_, _ = entryTable.WriteString("\n")
	}
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
	// 원본 파일 열기
	oldFile, err := os.OpenFile(entrytable_path, os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	defer oldFile.Close()

	// 임시 파일 생성
	tempPath := entrytable_path + ".tmp"
	newFile, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	defer newFile.Close()

	decoder := json.NewDecoder(oldFile)
	encoder := json.NewEncoder(newFile)

	// 한 줄씩 읽으면서 필터링
	for {
		var node NodeEntry
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		// 삭제 대상(Path가 일치하는 노드)이 아니면 새 파일에 씀
		if node.Path != targetNode {
			if err := encoder.Encode(node); err != nil {
				return err
			}
		}
	}

	// 파일 교체 (Atomic Rename)
	oldFile.Close()
	newFile.Close()

	return os.Rename(tempPath, entrytable_path)
}

func (f *Fs) Name() string           { return f.name }
func (f *Fs) Root() string           { return f.root }
func (f *Fs) String() string         { return f.name }
func (f *Fs) Features() *fs.Features { return f.features }

func (f *Fs) Move(ctx context.Context, src fs.Object, remote string) (fs.Object, error) {
	fmt.Printf("%s [%s] UNIC: Move: Move method Start. src: %s, remote: %s\n", time.Now().Format("15:04:05.000"), src.Remote(), src.Remote(), remote)

	srcObj, ok := src.(*Object)
	if !ok {
		return nil, fs.ErrorCantMove
	}

	srcPath := srcObj.remote

	oldFile, err := os.OpenFile(entrytable_path, os.O_RDWR, 0755)
	if err != nil {
		return nil, err
	}
	defer oldFile.Close()

	tempPath := entrytable_path + ".tmp." + fmt.Sprintf("%d", time.Now().UnixNano())
	newFile, err := os.Create(tempPath)
	if err != nil {
		return nil, err
	}
	defer newFile.Close()

	decoder := json.NewDecoder(oldFile)
	encoder := json.NewEncoder(newFile)

	var movedNode *NodeEntry

	for {
		var node NodeEntry
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		if node.Path == srcPath && node.Type == "file" {
			node.Path = remote
			node.Name = filepath.Base(remote)
			nodeCopy := node
			movedNode = &nodeCopy
		}

		if err := encoder.Encode(node); err != nil {
			return nil, err
		}
	}

	if movedNode == nil {
		os.Remove(tempPath)
		return nil, fs.ErrorObjectNotFound
	}

	oldFile.Close()
	newFile.Close()

	if err := os.Rename(tempPath, entrytable_path); err != nil {
		os.Remove(tempPath)
		return nil, err
	}

	// Rename local cache file
	oldLocalPath, err1 := f.MakeOSDownloadPath(srcPath)
	newLocalPath, err2 := f.MakeOSDownloadPath(remote)
	if err1 == nil && err2 == nil {
		if _, err := os.Stat(oldLocalPath); err == nil {
			// Ensure the destination directory exists
			os.MkdirAll(filepath.Dir(newLocalPath), 0755)
			// Rename the local cache
			if err := os.Rename(oldLocalPath, newLocalPath); err != nil {
				fmt.Printf("UNIC: Move: failed to rename local cache file from %s to %s: %v\n", oldLocalPath, newLocalPath, err)
			} else {
				fmt.Printf("UNIC: Move: successfully renamed local cache file from %s to %s\n", oldLocalPath, newLocalPath)
			}
		}
	}

	return f.newObject(ctx, remote, movedNode)
}

func (f *Fs) DirMove(ctx context.Context, src fs.Fs, srcRemote, dstRemote string) error {
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

	decoder := json.NewDecoder(oldFile)
	encoder := json.NewEncoder(newFile)

	for {
		var node NodeEntry
		if err := decoder.Decode(&node); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		if node.Path == srcRemote || isUnderDir(node.Path, srcRemote) {
			if node.Path == srcRemote {
				node.Path = dstRemote
				node.Name = filepath.Base(dstRemote)
			} else {
				node.Path = dstRemote + "/" + strings.TrimPrefix(node.Path, srcRemote+"/")
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
	o.modTime = t
	return nil
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
	//// dis_rm 수행하여 기존의 파일 삭제
	//fs.Debugf(o, "----------Update method start----------")

	//// dis_rm 수행
	//fs.Debugf(o, "----------dis_operations.Dis_rm start----------")
	//err := dis_operations.Dis_rm([]string{o.remote}, false)
	//if err != nil {
	//	return err
	//}
	//fs.Debugf(o, "----------dis_operations.Dis_rm end----------")

	//// dis_upload 수행하여 새로운 파일 업로드
	//// Create a temporary directory
	//fs.Debugf(o, "----------Create a temporary directory start--------------")
	//tempDir, err := os.MkdirTemp("", "unic_upload")
	//if err != nil {
	//	return err
	//}
	//defer os.RemoveAll(tempDir) // Clean up
	//fs.Debugf(o, "----------Create a temporary directory end--------------")

	//// Create the file with the correct name
	//fs.Debugf(o, "----------Create a temporary file start--------------")
	//tempFilePath := filepath.Join(tempDir, filepath.Base(src.Remote()))
	//fs.Debugf(o, "src.Remote(): %s", src.Remote())
	//fs.Debugf(o, "tempFilePath: %s", tempFilePath)
	//tempFile, err := os.Create(tempFilePath)
	//if err != nil {
	//	return err
	//}
	//fs.Debugf(o, "----------Create a temporary file end--------------")

	//// Copy content
	//fs.Debugf(o, "----------Copy content to temp file start--------------")
	//_, err = io.Copy(tempFile, in)

	//tempFile.Sync()
	//closeErr := tempFile.Close()
	//if err != nil {
	//	return err
	//}
	//if closeErr != nil {
	//	return closeErr
	//}
	//fs.Debugf(o, "----------Copy content to temp file end--------------")

	//// Call Dis_Upload
	//// args[0] is the file path. reSignal is false. LoadBalancer is RoundRobin (default).
	//fs.Debugf(o, "----------dis_upload start--------------")
	//fs.Debugf(o, "tempFilePath: %s, remotes: %s", tempFilePath, o.fs.getUpstreamRemotes())
	//fileId := o.id
	//remotePath := o.remote
	//err = dis_operations.Dis_Upload([]string{tempFilePath, fileId, remotePath}, dis_operations.UploadTargets{Remotes: o.fs.getUpstreamRemotes(), UseConfig: false}, false, dis_operations.RoundRobinFromSelectedRemotes)
	//if err != nil {
	//	return err
	//}
	//fs.Debugf(o, "----------dis_upload end--------------")
	//fs.Debugf(o, "----------Update method end--------------")

	return nil
}

// UNIC을 마운팅한 mount point에 unlink system call이 들어올 때 실행
func (o *Object) Remove(ctx context.Context) error {
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
