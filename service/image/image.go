package image

import (
	"GopherAI/common/image"
	"io"
	"log"
	"mime/multipart"
	"os"
)

func RecognizeImage(file *multipart.FileHeader) (string, error) {

	modelPath := os.Getenv("IMAGE_MODEL_PATH")
	if modelPath == "" {
		modelPath = "/root/models/mobilenetv2/mobilenetv2-7.onnx"
	}

	labelPath := os.Getenv("IMAGE_LABEL_PATH")
	if labelPath == "" {
		labelPath = "/root/imagenet_classes.txt"
	}
	inputH, inputW := 224, 224

	recognizer, err := image.NewImageRecognizer(modelPath, labelPath, inputH, inputW)
	if err != nil {
		log.Println("NewImageRecognizer fail err is : ", err)
		return "", err
	}
	defer recognizer.Close()

	src, err := file.Open()
	if err != nil {
		log.Println("file open fail err is : ", err)
		return "", err
	}
	defer src.Close()

	buf, err := io.ReadAll(src)
	if err != nil {
		log.Println("io.ReadAll fail err is : ", err)
		return "", err
	}

	return recognizer.PredictFromBuffer(buf)
}
