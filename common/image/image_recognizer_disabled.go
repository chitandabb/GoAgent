//go:build !onnx

package image

import (
	"errors"
	"image"
)

var ErrONNXDisabled = errors.New("image recognition is disabled; rebuild with the onnx tag and install the ONNX Runtime library")

type ImageRecognizer struct{}

func NewImageRecognizer(string, string, int, int) (*ImageRecognizer, error) {
	return nil, ErrONNXDisabled
}

func (*ImageRecognizer) Close() {}

func (*ImageRecognizer) PredictFromFile(string) (string, error) {
	return "", ErrONNXDisabled
}

func (*ImageRecognizer) PredictFromBuffer([]byte) (string, error) {
	return "", ErrONNXDisabled
}

func (*ImageRecognizer) PredictFromImage(image.Image) (string, error) {
	return "", ErrONNXDisabled
}
