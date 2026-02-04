package unic

import (
	"context"

	"github.com/rclone/rclone/fs/rc"
)

func init() {
	rc.Add(rc.Call{
		Path:         "unic/getID",
		AuthRequired: true,
		Fn:           getID,
		Title:        "get ID from rclone frontend",
		Help:         "get ID from rclone frontend",
	})
}

func getID(ctx context.Context, in rc.Params) error {
	// clinet가 전송한 데이터에서 ID 추출
	id, err := in.GetString("ID")

}
