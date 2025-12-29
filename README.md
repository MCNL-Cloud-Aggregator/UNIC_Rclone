
[<img src="https://rclone.org/img/logo_on_light__horizontal_color.svg" width="50%" alt="rclone logo">](https://rclone.org/#gh-light-mode-only)
[<img src="https://rclone.org/img/logo_on_dark__horizontal_color.svg" width="50%" alt="rclone logo">](https://rclone.org/#gh-dark-mode-only)

[Website](https://rclone.org) |
[Documentation](https://rclone.org/docs/) |
[Download](https://rclone.org/downloads/) |
[Contributing](CONTRIBUTING.md) |
[Changelog](https://rclone.org/changelog/) |
[Installation](https://rclone.org/install/) |
[Forum](https://forum.rclone.org/)

[![Build Status](https://github.com/rclone/rclone/workflows/build/badge.svg)](https://github.com/rclone/rclone/actions?query=workflow%3Abuild)
[![Go Report Card](https://goreportcard.com/badge/github.com/rclone/rclone)](https://goreportcard.com/report/github.com/rclone/rclone)
[![GoDoc](https://godoc.org/github.com/rclone/rclone?status.svg)](https://godoc.org/github.com/rclone/rclone)
[![Docker Pulls](https://img.shields.io/docker/pulls/rclone/rclone)](https://hub.docker.com/r/rclone/rclone)

# Rclone

다양한 Public Cloud Storage 서비스들을 연동시켜서 파일 업로드를 가능하게 하는 오픈소스
<br> <br>
Rclone에 대한 자세한 정보는 위 링크 참조 부탁드립니다.

## Storage providers

Rclone 이 지원하는 Public Cloud Storage 목록들 [the full list of all storage providers and their features](https://rclone.org/overview/)

# 기초 환경 구축 및 설치 방법
## 1. Cloud Aggregator 설치
```
git clone https://github.com/HandongSF/cloud_storage.git
```
## 2. Go version 1.21 이상 설치
설치
https://go.dev/dl/
```
brew install go
```
설치 확인
```
go version
```

## 3. Rclone build
클론 완료한 폴더 이동 후 rclone build 
```
go build -o rclone
```
빌드 확인
```
./rclone version
```
전역에서 쓸 수 있게 변수 설정

MAC
```
sudo cp ./rclone /usr/local/bin/
```
```
sudo chmod +x /usr/local/bin/rclone
```

WINDOW

시스템 환경변수 편집 -> 환경변수 -> 변수:PATH 클릭 -> 편집 -> rclone 경로를 추가
### 전역에서 사용이 불가능한 경우
임시적으로 rclone 실행 파일 생성 후 ./rclone ~~ 로 실행하시면 됩니다.
```
go build -o rclone
```
## 4. Public Storage 연결
rclone config 명령어 실행 후 안내되는 사항에 따라서 진행하시면 됩니다.
```
rclone config
```

# 각 명령어 실행 방법
## 1. dis_upload (분산 업로드)
```
rclone dis_upload [파일경로] -b [알고리즘 이름]
알고리즘 이름 : RoundRobin, ResourceBased, DownloadOptima, UploadOptima 중 택 1(대소문자 상관 있습니다)
```
## 2. dis_download (분산 다운로드)
```
rclone dis_download [파일 이름] [다운 받을 로컬 위치 경로]
```
## 3. dis_ls (분산 업로드 된 파일 조회)
```
rclone dis_ls
```
## 4. dis_rm (분산 업로드 된 파일 삭제)
```
rclone dis_rm [파일 이름]
```
## 5. dis_config (설정 파일 이동)
```
rclone dis_config [복사해온 설정 파일의 주소]
```

# cmd 파일 추가 script
## 1. dis_config
```
CLOUD_STORAGE/cmd/dis_config/dis_config_upload.go
```
내용: CLI에서 실행 가능한 dis_config 명령어 정의

## 2. dis_download
```
CLOUD_STORAGE/cmd/dis_download/dis_download.go
```
내용: CLI에서 실행 가능한 dis_download 명령어 정의

## 3. dis_ls
```
CLOUD_STORAGE/cmd/dis_ls/dis_ls.go
```
내용: CLI에서 실행 가능한 dis_ls 명령어 정의

## 3. dis_rm
```
CLOUD_STORAGE/cmd/dis_rm/dis_rm.go
```
내용: CLI에서 실행 가능한 dis_rm 명령어 정의

## 3. dis_upload
```
CLOUD_STORAGE/cmd/dis_upload/dis_upload.go
```
내용: CLI에서 실행 가능한 dis_upload 명령어 정의


# dis_operations
```
CLOUD_STORAGE/fs/dis_operations/datamap.go
```

이 dis_operations 패키지는 로컬에 저장된 datamap.json 파일을 통해 원본 파일과 그에 대응하는 분산 파일들의 메타데이터를 생성, 조회, 갱신하는 기능을 수행한다. 
주요 기능으로는 SHA-256 기반 체크섬 계산을 통한 파일 무결성 검증, 원본 파일의 이름, 크기, 패딩, 조각 수 등의 정보와 각 분산 파일의 저장 위치(Remote), 체크 여부 등을 구조체로 기록하고 관리하는 기능, 작업 상태(upload, delete 등)와 비정상 종료 여부를 추적하는 플래그 시스템, 작업 도중 중단된 경우 이어받기 위한 분산 파일 조회, 특정 파일 정보 삭제 및 초기화 등의 조작이 포함된다. 
또한 파일 이름과 해시 간 매핑, 작업 도중 남은 미완료 분산 파일 목록 확인, 각 조각의 체크 상태 및 저장 위치를 실시간으로 업데이트할 수 있도록 지원하며, 전체 JSON 파일 입출력에 대해 동기화를 위해 뮤텍스를 사용하여 동시성 제어도 수행한다.

# dis_download
```
CLOUD_STORAGE/fs/dis_operations/dis_download.go
```

이 코드는 Rclone 기반 분산 저장 시스템에서 분산 파일들을 병렬로 다운로드하고 Reed-Solomon 복원을 통해 원본 파일을 재구성하는 기능을 수행한다. 
Dis_Download 함수는 다운로드 작업의 시작점으로, 상태 플래그를 업데이트하고 필요 시 미완료된 파일 조각만을 선택해 다운로드하며, 고루틴 워커 풀을 이용해 병렬로 조각 파일을 다운로드한 뒤 지정한 경로에 원본 파일을 복원하고, 성공 시 관련 플래그를 초기화하고 조각 파일을 삭제한다. 
다운로드 과정에서는 Rclone 명령을 커맨드로 실행하여 원격 저장소에서 파일을 가져오고, 전송 속도(throughput)를 계산해 해당 원격지 성능 정보도 갱신한다. 동시 작업을 위해 sync.WaitGroup, sync.Mutex를 사용해 고루틴 간 동기화를 수행하며, 작업 중 오류가 발생할 경우 리스트에 수집하여 적절한 처리를 한다.


# dis_interaction
```
CLOUD_STORAGE/fs/dis_operations/dis_interaction.go
```
이 코드는 분산 파일 시스템에서 사용자 입력을 통해 파일 덮어쓰기, 삭제, 재업로드, 재다운로드 여부를 확인하는 인터페이스를 제공하며, ShowDescription_DoOverwrite와 ShowDescription_RemoveFile은 상황별 메시지를 출력하고 사용자에게 확인을 요청하고, AskDestination은 다운로드 경로를 입력받으며, 내부적으로 GetUserConfirmation은 기본 선택지와 함께 사용자 입력을 받아 예/아니오 결정을 내리고, 이를 바탕으로 DoOverwrite, DoRemove, DoReUpload, DoReDownload 함수들이 각각 덮어쓰기, 삭제, 재업로드, 재다운로드 동작에 대한 사용자 결정을 반환하는 역할을 수행한다.

# dis_loadbalance
```
CLOUD_STORAGE/fs/dis_operations/dis_loadbalance.go
```
여러 원격 저장소(Remote) 간의 부하 분산(load balancing)을 위해 다양한 전략(RoundRobin, DownloadOptima, UploadOptima, ResourceBased)을 구현하며, 각 원격 저장소의 상태 정보를 JSON 파일로 관리한다. 원격 저장소의 처리량이나 저장 공간 같은 성능 지표를 수집하고 이를 바탕으로 최적의 원격 저장소를 선택하며, rclone 라이브러리와 cobra 커맨드 프레임워크를 활용해 원격 저장소의 사용량 정보를 조회하는 커맨드(about)를 호출해 동적으로 상태를 갱신한다.


# dis_metadata
```
CLOUD_STORAGE/fs/dis_operations/dis_metadata.go
```
이 코드는 분산 파일 저장 시스템에서 각 파일 조각(DistributedFile)에 대해 부하 분산(load balancing) 전략을 사용해 원격 저장소(Remote)를 할당하는 기능을 제공한다. FileInfo와 DistributedFile 구조체로 파일과 분산된 파일 정보를 관리하며, RemoteInfo는 업로드 및 다운로드 처리량 기록과 최대 처리량 계산 기능을 포함한다. AllocateRemote 메서드는 지정된 부하 분산 타입(RoundRobin, DownloadOptima, UploadOptima, ResourceBased 등)에 따라 적절한 원격 저장소를 선택하여 분산 파일에 할당한다.

# dis_password
```
CLOUD_STORAGE/fs/dis_operations/dis_password.go
```
이 코드는 지정된 경로 내 파일들을 암호화 및 복호화하는 기능을 제공하며, 사용자 비밀번호를 안전하게 생성, 저장, 확인하는 로직을 포함한다. 먼저 tryGetPassword는 비밀번호 파일이 없으면 랜덤 비밀번호를 생성해 저장하고, GetUserPassword는 저장된 비밀번호를 읽어온다. EncryptAllFilesInPath와 DecryptAllFilesInPath 함수는 지정 경로의 모든 파일을 재귀적으로 암호화 또는 복호화하며, 복호화 시에는 특별히 "user_password"를 포함하는 파일로 비밀번호를 검증해 맞으면 암호화된 파일을 삭제하고, 틀리면 복호화된 잘못된 파일들을 삭제하여 보안을 유지합니다. 이 과정에서 외부 라이브러리 filecrypt를 이용해 실제 암복호화를 수행하며, 암호화된 파일은 .fcef 확장자를 갖는다.


# dis_rm
```
CLOUD_STORAGE/fs/dis_operations/dis_rm.go
```
이 코드는 분산 저장된 파일의 여러 조각(DistributedFile)을 병렬로 삭제하는 기능을 수행한다. Dis_rm 함수는 원본 파일명으로 메타데이터와 분산 파일 정보를 불러와 삭제 플래그를 업데이트하고, startRmFileGoroutine에서 각 분산 파일 조각별로 고루틴을 띄워 원격 저장소에서 파일을 삭제한다. 
삭제는 remoteCallDeleteFile 함수가 내부적으로 cobra.Command로 정의된 deleteFileDefinition 명령어를 실행해 수행하며, rclone의 원격 파일 삭제 기능을 활용한다. 삭제 성공 시 각 분산 파일에 대한 체크 플래그를 갱신하고, 모든 삭제가 끝난 후 원본 파일 메타데이터에서 해당 파일 정보를 제거합니다. 에러 발생 시 여러 에러를 모아 반환하고, 삭제 처리 시간도 출력해 작업 진행 상황을 알 수 있도록 설계되어 있다.

# dis_status
```
CLOUD_STORAGE/fs/dis_operations/dis_status.go
```
이 코드는 분산 저장 작업 중에 미완료 상태(업로드, 다운로드, 삭제)가 있으면 그 상태에 따라 적절히 처리하거나 이전 작업을 중단·복구하는 기능을 구현한다. CheckState 함수는 미완료 작업 플래그와 상태를 검사해, 만약 미완료 작업이 있으면 상태별로 upload, download, rm의 재시도 혹은 중단 처리 로직을 실행한다. 
업로드나 다운로드 중단 시 DumpUploadState, DumpDownloadState로 해당 조각(샤드)을 삭제하거나 상태를 초기화하고, 삭제 중단 시 DumpRmState로 남은 조각을 삭제한다. 샤드 삭제는 DumpUploadShards와 DumpDownloadShards에서 reedsolomon.DeleteShardWithFileNames 함수를 호출해 수행하며, 작업 명령과 인자가 동일한지 확인하는 checkSameCommand도 포함되어 있어 재시도 여부 판단에 활용된다.

# dis_upload
```
CLOUD_STORAGE/fs/dis_operations/dis_upload.go
```
이 코드는 Rclone을 활용해 분산 저장 시스템에 파일을 업로드하는 기능을 구현하며, 입력받은 파일을 Reed-Solomon 인코딩으로 샤딩하고, 여러 원격 저장소에 병렬로 분산 업로드한다. 업로드 작업은 고루틴 워커 풀이나 개별 고루틴으로 처리되며, 각 샤드별 원격 할당과 복사 작업, 처리량 계산 및 오류 관리를 포함한다. 또한, 업로드 전 원격 저장소에 필요한 디렉터리를 생성하고, 파일 중복 검사 및 해시 이름 생성, 업로드 후 상태 업데이트까지 전체 분산 업로드 프로세스를 체계적으로 관리한다.

# reedsolomon
```
CLOUD_STORAGE/reedsolomon/streaming.go
```
이 코드는 원본 파일을 업로드를 위한 작은 파일로 나누는 기능과 더불어 다시 다운로드 후 원본 파일로 복구 할 때 사용되는 함수들이 있습니다. 파일 크기에 따라 조각 내는 개수를 정하는 함수 및 encode, decode과정 중 파일에 대한 암호화와 복호화가 이루어 지고 있습니다. decode중 다운로드 받을 때 parity shard의 개수보다 많은 손실이 이루어지는 경우 파일의 복구가 이루어지지 않으며 그 외의 경우에는 파일의 복구가 이루어 질 수 있습니다.

