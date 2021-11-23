package ctrlrpc

import (
	"encoding/base64"
	"fmt"
	"strings"
)

const (
	UrlConfKey            = "externUrl"
	AddressConfKey        = "address"
	GcScanPeriodKey       = "gcPeriod"
	GcCollectThresholdKey = "gcThreshold"
)

func EncodeAWSCred(awsId, awsSecret string) string {
	return fmt.Sprintf("%s:%s", base64.RawStdEncoding.EncodeToString([]byte(awsId)), base64.RawStdEncoding.EncodeToString([]byte(awsSecret)))
}

func DecodeAWSCred(s string) (awsId, awsSecret string, err error) {
	if s := strings.SplitN(s, ":", 2); len(s) != 2 {
		err = fmt.Errorf("invalid credential, missing ':' separator")
	} else if bId, e := base64.RawStdEncoding.DecodeString(s[0]); e != nil {
		err = fmt.Errorf("invalid aws access key id, %w", e)
	} else if bKey, e := base64.RawStdEncoding.DecodeString(s[1]); e != nil {
		err = fmt.Errorf("invalid aws access key secret, %w", e)
	} else if awsId, awsSecret = string(bId), string(bKey); len(strings.TrimSpace(awsId)) == 0 || len(strings.TrimSpace(awsSecret)) == 0 {
		err = fmt.Errorf("aws access key id/secret can't be empty")
	}
	return
}
