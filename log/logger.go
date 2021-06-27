package log

import "github.com/golang/glog"

func Info(format string, args ...interface{}) {
	glog.Infof(format, args...)
}
