package metadata

import (
	"encoding/base64"
	"strings"
)

type MD map[string][]string

func DecodeKeyValue(k, v string) (string, string, error) {
	valueSlice := strings.Split(v, ",")
	for i, vv := range valueSlice {
		val, err := base64.StdEncoding.DecodeString(vv)
		if err != nil {
			return "", "", err
		}

		valueSlice[i] = string(val)
	}
	return k, strings.Join(valueSlice, ","), nil
}
