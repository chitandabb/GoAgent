package onnxlayout

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

var environmentState struct {
	sync.Mutex
	path string
	refs int
}

type ortEngine struct {
	session     *ort.DynamicAdvancedSession
	inputWidth  int64
	inputHeight int64
	releaseEnv  func() error
	closeOnce   sync.Once
	closeErr    error
}

func newORTEngine(config Config) (*ortEngine, error) {
	release, err := acquireEnvironment(config.RuntimeLibraryPath)
	if err != nil {
		return nil, err
	}
	options, err := ort.NewSessionOptions()
	if err != nil {
		release()
		return nil, fmt.Errorf("create ONNX Runtime session options: %w", err)
	}
	keepEnvironment := false
	defer func() {
		_ = options.Destroy()
		if !keepEnvironment {
			_ = release()
		}
	}()
	if err := options.SetExecutionMode(ort.ExecutionModeSequential); err != nil {
		return nil, fmt.Errorf("set ONNX Runtime execution mode: %w", err)
	}
	if err := options.SetGraphOptimizationLevel(ort.GraphOptimizationLevelEnableAll); err != nil {
		return nil, fmt.Errorf("set ONNX Runtime graph optimization: %w", err)
	}
	if err := options.SetIntraOpNumThreads(config.IntraOpThreads); err != nil {
		return nil, fmt.Errorf("set ONNX Runtime intra-op threads: %w", err)
	}
	if err := options.SetInterOpNumThreads(config.InterOpThreads); err != nil {
		return nil, fmt.Errorf("set ONNX Runtime inter-op threads: %w", err)
	}
	session, err := ort.NewDynamicAdvancedSession(
		config.ModelPath,
		[]string{"image", "scale_factor"},
		[]string{"fetch_name_0", "fetch_name_1"},
		options,
	)
	if err != nil {
		return nil, fmt.Errorf("create ONNX layout session: %w", err)
	}
	keepEnvironment = true
	return &ortEngine{
		session: session, inputWidth: int64(config.InputWidth), inputHeight: int64(config.InputHeight),
		releaseEnv: release,
	}, nil
}

func acquireEnvironment(libraryPath string) (func() error, error) {
	absolutePath, err := filepath.Abs(libraryPath)
	if err != nil {
		return nil, fmt.Errorf("resolve ONNX Runtime library path: %w", err)
	}
	environmentState.Lock()
	defer environmentState.Unlock()
	if environmentState.refs > 0 {
		if environmentState.path != absolutePath {
			return nil, errors.New("ONNX Runtime is already initialized with a different library")
		}
		environmentState.refs++
		return releaseEnvironment, nil
	}
	if ort.IsInitialized() {
		return nil, errors.New("ONNX Runtime environment is already owned outside the layout adapter")
	}
	ort.SetSharedLibraryPath(absolutePath)
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("initialize ONNX Runtime: %w", err)
	}
	environmentState.path = absolutePath
	environmentState.refs = 1
	return releaseEnvironment, nil
}

func releaseEnvironment() error {
	environmentState.Lock()
	defer environmentState.Unlock()
	if environmentState.refs < 1 {
		return errors.New("ONNX Runtime environment reference count is invalid")
	}
	environmentState.refs--
	if environmentState.refs > 0 {
		return nil
	}
	environmentState.path = ""
	return ort.DestroyEnvironment()
}

func (e *ortEngine) Run(ctx context.Context, imageData, scaleData []float32) ([]float32, []int32, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	imageTensor, err := ort.NewTensor(ort.NewShape(1, 3, e.inputHeight, e.inputWidth), imageData)
	if err != nil {
		return nil, nil, fmt.Errorf("create ONNX image tensor: %w", err)
	}
	defer imageTensor.Destroy()
	scaleTensor, err := ort.NewTensor(ort.NewShape(1, 2), scaleData)
	if err != nil {
		return nil, nil, fmt.Errorf("create ONNX scale tensor: %w", err)
	}
	defer scaleTensor.Destroy()
	runOptions, err := ort.NewRunOptions()
	if err != nil {
		return nil, nil, fmt.Errorf("create ONNX run options: %w", err)
	}
	defer runOptions.Destroy()

	stopWatch := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			_ = runOptions.Terminate()
		case <-stopWatch:
		}
	}()
	outputs := []ort.Value{nil, nil}
	err = e.session.RunWithOptions([]ort.Value{imageTensor, scaleTensor}, outputs, runOptions)
	close(stopWatch)
	<-watchDone
	defer destroyValues(outputs)
	if err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	boxes, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, nil, errors.New("ONNX layout boxes output has an unexpected type")
	}
	counts, ok := outputs[1].(*ort.Tensor[int32])
	if !ok {
		return nil, nil, errors.New("ONNX layout counts output has an unexpected type")
	}
	return append([]float32(nil), boxes.GetData()...), append([]int32(nil), counts.GetData()...), nil
}

func destroyValues(values []ort.Value) {
	for _, value := range values {
		if value != nil {
			_ = value.Destroy()
		}
	}
}

func (e *ortEngine) Close() error {
	if e == nil {
		return nil
	}
	e.closeOnce.Do(func() {
		if e.session != nil {
			e.closeErr = e.session.Destroy()
		}
		if e.releaseEnv != nil {
			e.closeErr = errors.Join(e.closeErr, e.releaseEnv())
		}
	})
	return e.closeErr
}
