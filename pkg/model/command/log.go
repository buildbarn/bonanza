package command

import (
	"context"
	"slices"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_command_pb "bonanza.build/pkg/proto/model/command"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Names of logs that are captured for every command.
const (
	StdoutLogName = "stdout"
	StderrLogName = "stderr"
)

// GetLog returns the log having a given name from the outputs of a
// command. As logs are required to be sorted by name, a binary search
// is performed. If the log is not present, an unset message is
// returned.
func GetLog[TReference any](outputs model_core.Message[*model_command_pb.Outputs, TReference], name string) model_core.Message[*model_command_pb.Log, TReference] {
	logs := outputs.Message.GetLogs()
	if i, ok := slices.BinarySearchFunc(logs, name, func(log *model_command_pb.Log, name string) int {
		return strings.Compare(log.Name, name)
	}); ok {
		return model_core.Nested(outputs, logs[i])
	}
	return model_core.Message[*model_command_pb.Log, TReference]{}
}

// ReadLog reads the full contents of a log. An error is returned if
// the size of the log exceeds the provided maximum.
func ReadLog[TReference object.BasicReference](ctx context.Context, fileReader *model_filesystem.FileReader[TReference], log model_core.Message[*model_command_pb.Log, TReference], maximumSizeBytes uint64) ([]byte, error) {
	entry, err := model_filesystem.NewFileContentsEntryFromProto(model_core.Nested(log, log.Message.Contents))
	if err != nil {
		return nil, util.StatusWrap(err, "Invalid contents")
	}
	return fileReader.FileReadAll(ctx, entry, maximumSizeBytes)
}

// ReadLogByName is a helper function for reading the full contents of
// a log having a given name from the outputs of a command. An error is
// returned if the log is not present.
func ReadLogByName[TReference object.BasicReference](ctx context.Context, fileReader *model_filesystem.FileReader[TReference], outputs model_core.Message[*model_command_pb.Outputs, TReference], name string, maximumSizeBytes uint64) ([]byte, error) {
	log := GetLog(outputs, name)
	if !log.IsSet() {
		return nil, status.Errorf(codes.NotFound, "Outputs do not contain log %#v", name)
	}
	return readLog(ctx, fileReader, log, name, maximumSizeBytes)
}

// MaybeReadLogByName is identical to ReadLogByName, except that it
// returns empty contents if the log is not present. This can be used
// in cases where the absence of a log should not be treated as an
// error, such as when the command failed to launch, causing standard
// output and standard error not to be captured.
func MaybeReadLogByName[TReference object.BasicReference](ctx context.Context, fileReader *model_filesystem.FileReader[TReference], outputs model_core.Message[*model_command_pb.Outputs, TReference], name string, maximumSizeBytes uint64) ([]byte, error) {
	log := GetLog(outputs, name)
	if !log.IsSet() {
		return nil, nil
	}
	return readLog(ctx, fileReader, log, name, maximumSizeBytes)
}

func readLog[TReference object.BasicReference](ctx context.Context, fileReader *model_filesystem.FileReader[TReference], log model_core.Message[*model_command_pb.Log, TReference], name string, maximumSizeBytes uint64) ([]byte, error) {
	data, err := ReadLog(ctx, fileReader, log, maximumSizeBytes)
	if err != nil {
		return nil, util.StatusWrapf(err, "Failed to read log %#v", name)
	}
	return data, nil
}
