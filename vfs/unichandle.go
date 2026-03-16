package vfs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"time"

	"github.com/rclone/rclone/backend/unic"
	"github.com/rclone/rclone/fs"
)

// UnicFileHandle is an open for write handle on a File
type UnicFileHandle struct {
	baseHandle
	mu          sync.Mutex
	cond        sync.Cond // cond lock for out of sequence writes
	remote      string
	o           fs.Object
	localFile   *os.File
	localPath   string
	file        *File
	offset      int64
	flags       int
	closed      bool // set if handle has been closed
	writeCalled bool // set the first time Write() is called
	opened      bool
	truncated   bool
	isDirty     bool
}

// Check interfaces
var (
	_ io.Writer   = (*UnicFileHandle)(nil)
	_ io.WriterAt = (*UnicFileHandle)(nil)
	_ io.Closer   = (*UnicFileHandle)(nil)
)

func newUnicFileHandle(d *Dir, f *File, flags int) (*UnicFileHandle, error) {
	fmt.Printf("[%s] %s unichandle: newUnicFileHandle: start\n", f.Path(), time.Now().Format("15:04:05.000"))
	lPath, _ := f.Fs().(*unic.Fs).MakeOSDownloadPath(f.Path())

	// 디렉토리가 없을 수 있으니 MkdirAll 수행
	os.MkdirAll(filepath.Dir(lPath), 0755)

	// O_TRUNC 플래그가 설정되어 있거나, 캐시와 원격 저장소 양쪽 모두에 파일이 없는 경우 빈 파일 강제 생성
	_, statErr := os.Stat(lPath)
	isLocalExist := statErr == nil

	if (flags&os.O_TRUNC != 0) || (!isLocalExist && !f.exists()) {
		tmpFile, err := os.Create(lPath)
		if err == nil {
			tmpFile.Close() // 빈 파일만 만들어두고 당장 닫음 (나중에 openPending에서 다시 열도록 둠)
		} else {
			// 에러 로깅
		}
	}

	fh := &UnicFileHandle{
		remote:    f.Path(),
		flags:     flags,
		file:      f,
		localPath: lPath,
		isDirty:   false,
	}

	fh.cond = sync.Cond{L: &fh.mu}
	if !fh.readOnly() {
		fh.file.addWriter(fh)
	}
	return fh, nil
}

// removeUnic handles file deletion for unic backend
func (f *File) removeUnic() error {
	fmt.Printf("[%s] %s unichandle: removeUnic: start\n", f.Path(), time.Now().Format("15:04:05.000"))
	f.CancelPendingUpload()

	// local cache 삭제
	if fsObj, ok := f.Fs().(*unic.Fs); ok {
		localPath, _ := fsObj.MakeOSDownloadPath(f.Path())
		fmt.Printf("[%s] unichandle: removeUnic: localPath: %s\n", f.Path(), localPath)
		err := os.Remove(localPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.o != nil {
		return f.o.Remove(context.TODO())
	}
	return nil
}
func (f *File) openUnic(flags int) (fh *UnicFileHandle, err error) {
	fmt.Printf("[%s] %s unichandle: openUnic: start\n", f.Path(), time.Now().Format("15:04:05.000"))
	fmt.Printf("[%s] unichandle: vfsFile.modtime: %v\n", f.Path(), f.ModTime())
	f.mu.RLock()
	d := f.d
	f.mu.RUnlock()
	// FIXME chunked
	if flags&accessModeMask != os.O_RDONLY && d.vfs.Opt.ReadOnly {
		return nil, EROFS
	}

	fh, err = newUnicFileHandle(d, f, flags)
	if err != nil {
		fs.Debugf(f.Path(), "File.openRW failed: %v", err)
		return nil, err
	}
	return fh, nil
}

// returns whether it is OK to truncate the file
func (fh *UnicFileHandle) safeToTruncate() bool {
	return fh.truncated || fh.flags&os.O_TRUNC != 0 || !fh.file.exists()
}

// openPending opens the file if there is a pending open
//
// call with the lock held
func (fh *UnicFileHandle) openPending() (err error) {
	fmt.Printf("[%s] %s unichandle: openPending: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	if fh.opened {
		return nil
	}
	fh.o = fh.file.getObject()

	// unic.go의 Open() 함수를 통한 기존 파일 다운로드 트리거
	// 단, 파일을 새로 쓰거나(O_TRUNC), 새로 만드는(O_CREATE) 모드이면서
	// 기존 내용을 무시해도 되는 상황이라면 다운로드를 건너뜀
	// 여기서는 안전하게 'Truncate' 플래그가 없을 때만 다운로드하도록 구성.
	if fh.o != nil && (fh.flags&os.O_TRUNC == 0) {
		rc, err := fh.o.Open(context.TODO())
		if err != nil {
			return fmt.Errorf("unic: openPending download failed: %w", err)
		}
		_ = rc.Close()
	}

	// O_APPEND 플래그가 활성화 되어있는 상황에서도 writeAt() 함수를 제대로 실행하도록 O_APPEND 플래그 제거
	fh.flags = fh.flags &^ os.O_APPEND

	// O_EXCL 플래그 제거: newUnicFileHandle에서 로컬 파일이 미리 생성된 경우 OpenFile 시 에러(file exists) 발생 방지
	fh.flags = fh.flags &^ os.O_EXCL

	// 실제 사용할 로컬 캐시 파일 오픈
	// 사용자가 요청한 flags(RDONLY, WRONLY, RDWR, APPEND, TRUNC)가 그대로 적용됨
	fh.localFile, err = os.OpenFile(fh.localPath, fh.flags, 0644)
	fmt.Printf("[%s] check file local path: %s\n", fh.remote, fh.localPath)
	if err != nil {
		return fmt.Errorf("unic: failed to final open with flags(%d): %w", fh.flags, err)
	}

	fh.opened = true
	return nil
}

// String converts it to printable
func (fh *UnicFileHandle) String() string {
	fmt.Printf("[%s] %s unichandle: String: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	if fh == nil {
		return "<nil *UnicWriteFileHandle>"
	}
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if fh.file == nil {
		return "<nil *UnicWriteFileHandle.file>"
	}
	return fh.file.String() + " (uw)"
}

// Node returns the Node associated with this - satisfies Noder interface
func (fh *UnicFileHandle) Node() Node {
	fmt.Printf("[%s] %s unichandle: Node: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	fh.mu.Lock()
	defer fh.mu.Unlock()
	return fh.file
}

// WriteAt writes len(p) bytes from p to the underlying data stream at offset
// off. It returns the number of bytes written from p (0 <= n <= len(p)) and
// any error encountered that caused the write to stop early. WriteAt must
// return a non-nil error if it returns n < len(p).
//
// If WriteAt is writing to a destination with a seek offset, WriteAt should
// not affect nor be affected by the underlying seek offset.
//
// Clients of WriteAt can execute parallel WriteAt calls on the same
// destination if the ranges do not overlap.
//
// Implementations must not retain p.
func (fh *UnicFileHandle) WriteAt(p []byte, off int64) (n int, err error) {
	fmt.Printf("[%s] %s unichandle: WriteAt: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	fh.mu.Lock()
	defer fh.mu.Unlock()
	return fh.writeAt(p, off)
}

// Implementation of WriteAt - call with lock held
func (fh *UnicFileHandle) writeAt(p []byte, off int64) (n int, err error) {
	fmt.Printf("[%s] %s unichandle: writeAt: start\n", fh.remote, time.Now().Format("15:04:05.000"))

	if fh.closed {
		return 0, ECLOSED
	}

	if err = fh.openPending(); err != nil {
		return 0, err
	}

	n, err = fh.localFile.WriteAt(p, off)

	if n > 0 {
		// 최초 쓰기 시에 VFS 디렉토리 캐시에 해당 파일을 즉시 추가 (ls 등에서 바로 보이도록)
		if !fh.isDirty {
			fh.file.d.addObject(fh.file)
		}

		fh.isDirty = true

		newOffset := off + int64(n)
		if newOffset > fh.file.Size() {
			fh.file.setSize(newOffset)
		}

		fh.offset = newOffset
		//fh.cond.Broadcast()
	}

	if err != nil {
		fs.Errorf(fh.remote, "UnicWriteFileHandle.WriteAt error: %v", err)
		return n, err
	}

	return n, nil
}

// Write writes len(p) bytes from p to the underlying data stream. It returns
// the number of bytes written from p (0 <= n <= len(p)) and any error
// encountered that caused the write to stop early. Write must return a non-nil
// error if it returns n < len(p). Write must not modify the slice data, even
// temporarily.
//
// Implementations must not retain p.
func (fh *UnicFileHandle) Write(p []byte) (n int, err error) {
	fmt.Printf("[%s] %s unichandle: Write: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	fh.mu.Lock()
	defer fh.mu.Unlock()
	// Since we can't seek, just call WriteAt with the current offset
	return fh.writeAt(p, fh.offset)
}

// WriteString a string to the file
func (fh *UnicFileHandle) WriteString(s string) (n int, err error) {
	fmt.Printf("[%s] %s unichandle: WriteString: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	return fh.Write([]byte(s))
}

// Offset returns the offset of the file pointer
func (fh *UnicFileHandle) Offset() (offset int64) {
	fmt.Printf("[%s] %s unichandle: Offset: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	fh.mu.Lock()
	defer fh.mu.Unlock()
	return fh.offset
}

// close the file handle returning EBADF if it has been
// closed already.
//
// Must be called with fh.mu held
func (fh *UnicFileHandle) close() (err error) {
	fmt.Printf("[%s] %s unichandle: close: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	if fh.closed {
		return ECLOSED
	}
	fh.closed = true

	if !fh.readOnly() {
		// leave writer open until file is transferred
		defer fh.file.delWriter(fh)
	}

	// If file not opened and not safe to truncate then leave file intact
	if !fh.opened {
		return nil
	}

	// 디스크 동기화 강제 실행
	if fh.localFile != nil {
		_ = fh.localFile.Sync()
	}

	if fh.isDirty {
		// 필요한 변수들을 백그라운드 고루틴으로 넘기기 위해 변수에 캡쳐
		remotePath := fh.remote
		localPath := fh.localPath
		vfsFile := fh.file
		unicFs := fh.file.Fs().(*unic.Fs)

		// 비동기 업로드 전, vfsFile의 메타데이터를 가져옴 (Vim 경고 방지)
		preservedModTime := vfsFile.ModTime()
		preservedSize := vfsFile.Size()

		// dis_upload 전에 메타데이터 기록
		vfsFile.SetModTime(preservedModTime)
		vfsFile.setSize(preservedSize)

		// 현재 로컬 파일 핸들은 즉시 닫아주어 Vim 등 다른 프로세스가 접근/삭제할 수 있게 놓아줌
		if closeErr := fh.localFile.Close(); closeErr != nil {
			fs.Errorf(fh.remote, "File Closing Failed: %v", closeErr)
		}

		fh.isDirty = false

		// 기존: 숨김파일, 백업파일일 경우 즉시 return nil (업로드 거부)
		// 변경: 우분투 편집기 등 Atomic Save 동작(임시숨김파일 -> Rename)을 고려하여
		// 5초 대기열에 무조건 넣되, 5초 뒤에 현재 경로가 여전히 숨김파일이면 그 때 업로드를 버림.
		isInitiallyHidden := strings.HasPrefix(filepath.Base(localPath), ".") || strings.HasSuffix(localPath, "~")
		if isInitiallyHidden {
			fmt.Printf("[%s] %s unichandle: close: hidden/backup file detected. queued for async check.\n", fh.remote, time.Now().Format("15:04:05.000"))
		}

		// 새로운 취소 가능한 컨텍스트 생성
		ctx, cancel := context.WithCancel(context.Background())
		vfsFile.mu.Lock()
		// 이미 대기 중인 업로드가 있다면 여기서 즉시 취소 (Lock 안에서 처리하여 레이스 방지)
		if vfsFile.cancelUpload != nil {
			vfsFile.cancelUpload()
		}
		vfsFile.cancelUpload = cancel
		vfsFile.mu.Unlock()

		// 백그라운드(비동기)에서 업로드를 수행하는 Write-Back Cache 로직 실행
		go func() {
			defer func() {
				cancel()
			}()

			// 5초 대기 (취소 가능)
			select {
			case <-time.After(5 * time.Second):
				fmt.Printf("[%s] unichandle: async upload start \n", remotePath)
			case <-ctx.Done():
				fmt.Printf("[%s] unichandle: async upload cancelled \n", remotePath)
				return
			}

			// 5초 대기 완료 후 실제 업로드 시작 시점
			vfsFile.mu.Lock()
			if vfsFile.nwriters.Load() > 0 {
				fmt.Printf("[%s] unichandle: async upload deferred (file still has active writers) \n", remotePath)
				vfsFile.cancelUpload = nil
				vfsFile.mu.Unlock()
				return
			}
			vfsFile.cancelUpload = nil
			vfsFile.mu.Unlock() // 데드락 방지! Path() 호출 전 무조건 언락해야 합니다.

			// 에디터의 rename 동작으로 인해 현재 파일 이름이 바뀌었을 수 있으므로 VFS File 객체의 최신 Path() 확인
			currentRemotePath := vfsFile.Path()

			if _, err := os.Stat(localPath); os.IsNotExist(err) {
				fmt.Printf("[%s] unichandle: async upload skipped (file deleted)\n", remotePath)
				return
			}

			fmt.Printf("[%s] unichandle: async upload started as [%s]\n", remotePath, currentRemotePath)

			// Put_ 을 위해 파일을 새로 엽니다.
			uploadFile, err := os.Open(localPath)
			if err != nil {
				fs.Errorf(remotePath, "async upload failed to re-open local cache: %v", err)
				return
			}

			// 바뀐 이름(currentRemotePath)으로 클라우드에 업로드 수행!
			newObj, uploadErr := unicFs.Put_(context.Background(), uploadFile, currentRemotePath)
			uploadFile.Close()
			if uploadErr != nil {
				fs.Errorf(currentRemotePath, "async upload failed: %v", uploadErr)
				return
			}

			// 성공 시 부모 VFS File 객체에 정보 덮어쓰기
			if newObj != nil && vfsFile != nil {
				newObj.SetModTime(context.Background(), preservedModTime)
				vfsFile.setObject(newObj)
				vfsFile.setSize(preservedSize)

				// 로컬 캐시 파일의 위치(이름)도 새로운 경로 이름에 맞게 Rename 처리 해주어,
				// 나중에 동일 파일을 접근 시 다시 다운로드되지 않도록 맞춰줌
				if currentRemotePath != remotePath {
					if newLocalPath, err := unicFs.MakeOSDownloadPath(currentRemotePath); err == nil {
						os.MkdirAll(filepath.Dir(newLocalPath), 0755)
						if renameErr := os.Rename(localPath, newLocalPath); renameErr != nil {
							fs.Debugf(currentRemotePath, "Failed to rename local cache file: %v", renameErr)
						}
					}
				}

				fmt.Printf("[%s] unichandle: vfsFile.modtime: %v\n", currentRemotePath, vfsFile.ModTime())
				fmt.Printf("[%s] unichandle: async upload complete: file size: %d\n", currentRemotePath, vfsFile.Size())
			}
		}()

		return nil
	}

	return nil
}

// Close closes the file
func (fh *UnicFileHandle) Close() error {
	fmt.Printf("[%s] %s unichandle: Close: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	fh.mu.Lock()
	defer fh.mu.Unlock()
	return fh.close()
}

// Flush is called on each close() of a file descriptor. So if a
// filesystem wants to return write errors in close() and the file has
// cached dirty data, this is a good place to write back data and
// return any errors. Since many applications ignore close() errors
// this is not always useful.
//
// NOTE: The flush() method may be called more than once for each
// open(). This happens if more than one file descriptor refers to an
// opened file due to dup(), dup2() or fork() calls. It is not
// possible to determine if a flush is final, so each flush should be
// treated equally. Multiple write-flush sequences are relatively
// rare, so this shouldn't be a problem.
//
// Filesystems shouldn't assume that flush will always be called after
// some writes, or that if will be called at all.
func (fh *UnicFileHandle) Flush() error {
	fmt.Printf("[%s] %s unichandle: Flush: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if fh.closed {
		fs.Debugf(fh.remote, "UnicFileHandle.Flush nothing to do")
		return nil
	}

	//로컬 파일 디스크 저장
	if fh.localFile != nil {
		err := fh.localFile.Sync()
		if err != nil {
			fs.Errorf(fh.remote, "Flush failed: %v", err)
			return err
		}
	}
	return nil
}

// Release is called when we are finished with the file handle
//
// It isn't called directly from userspace so the error is ignored by
// the kernel
func (fh *UnicFileHandle) Release() error {
	fmt.Printf("[%s] %s unichandle: Release: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if fh.closed {
		fs.Debugf(fh.remote, "UnicFileHandle.Release nothing to do")
		return nil
	}
	fs.Debugf(fh.remote, "UnicFileHandle.Release closing")
	err := fh.close()
	if err != nil {
		fs.Errorf(fh.remote, "UnicFileHandle.Release error: %v", err)
	}
	return err
}

// Stat returns info about the file
func (fh *UnicFileHandle) Stat() (os.FileInfo, error) {
	fmt.Printf("[%s] %s unichandle: Stat: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	fh.mu.Lock()
	defer fh.mu.Unlock()
	return fh.file, nil
}

// Truncate file to given size
func (fh *UnicFileHandle) Truncate(size int64) (err error) {
	fmt.Printf("[%s] %s unichandle: Truncate: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	fh.mu.Lock()
	defer fh.mu.Unlock()

	if err = fh.openPending(); err != nil {
		return err
	}

	fmt.Printf("[%s] unichandle: Truncate: size: %d\n", fh.remote, size)

	// 로컬 파일 포인터가 있는지 확인
	if fh.localFile == nil {
		return ECLOSED
	}

	// 실제 로컬 파일의 크기를 변경
	err = fh.localFile.Truncate(size)
	if err != nil {
		fs.Errorf(fh.remote, "Truncate 실패: %v", err)
		return err
	}

	// 수정되었으므로 dirty 플래그 설정
	fh.isDirty = true

	// 만약 현재 오프셋이 잘려나간 사이즈보다 크다면, 오프셋을 조정
	if fh.offset > size {
		fh.offset = size
	}

	return nil
}

func (fh *UnicFileHandle) Seek(offset int64, whence int) (int64, error) {
	fmt.Printf("[%s] %s unichandle: Seek: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	fh.mu.Lock()
	defer fh.mu.Unlock()

	if fh.closed {
		return 0, ECLOSED
	}

	// 아직 파일이 열리지 않았다면 (Lazy Load)
	if !fh.opened || fh.localFile == nil {
		// 읽기/쓰기 동작이 아니더라도 Seek이 들어오면 파일을 준비시키는 것이 가장 안전
		// 예를 들어 파일 끝으로 이동해서 크기를 알아내려는 동작 등이 있을 수 있기 때문
		if err := fh.openPending(); err != nil {
			return 0, err
		}
	}

	// 실제 로컬 파일의 포인터를 이동
	// whence: 0(시작점 기준), 1(현재 위치 기준), 2(파일 끝 기준)
	newOffset, err := fh.localFile.Seek(offset, whence)
	if err != nil {
		return 0, err
	}

	// 핸들러의 오프셋을 최신화
	fh.offset = newOffset
	return newOffset, nil
}

// Read reads up to len(p) bytes into p.
func (fh *UnicFileHandle) Read(p []byte) (n int, err error) {
	fmt.Printf("[%s] %s unichandle: Read: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	fh.mu.Lock()
	defer fh.mu.Unlock()
	n, err = fh.readAt(p, fh.offset)
	fh.offset += int64(n)
	return n, err
}

// ReadAt reads len(p) bytes into p starting at offset off in the
// underlying input source. It returns the number of bytes read (0 <=
// n <= len(p)) and any error encountered.
func (fh *UnicFileHandle) ReadAt(p []byte, off int64) (n int, err error) {
	fmt.Printf("[%s] %s unichandle: ReadAt: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	fh.mu.Lock()
	defer fh.mu.Unlock()
	return fh.readAt(p, off)
}

func (fh *UnicFileHandle) readAt(p []byte, off int64) (n int, err error) {
	fmt.Printf("[%s] %s unichandle: readAt: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	if err := fh.openPending(); err != nil {
		return 0, err
	}

	if fh.localFile == nil {
		return 0, ECLOSED
	}

	fmt.Printf("[%s] Check readAt\n", fh.remote)
	return fh.localFile.ReadAt(p, off)
}

func (fh *UnicFileHandle) readOnly() bool {
	fmt.Printf("[%s] %s unichandle: readOnly: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	return (fh.flags & accessModeMask) == os.O_RDONLY
}

// Sync commits the current contents of the file to stable storage. Typically,
// this means flushing the file system's in-memory copy of recently written
// data to disk.
func (fh *UnicFileHandle) Sync() error {
	fmt.Printf("[%s] %s unichandle: Sync: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if fh.closed {
		return ECLOSED
	}
	if !fh.opened {
		return nil
	}
	if fh.readOnly() {
		return nil
	}
	return fh.localFile.Sync()
}

// Name returns the name of the file from the underlying Object.
func (fh *UnicFileHandle) Name() string {
	fmt.Printf("[%s] %s unichandle: Name: start\n", fh.remote, time.Now().Format("15:04:05.000"))
	return fh.file.String()
}
