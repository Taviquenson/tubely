package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	// Authenticate the user
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}
	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	// Get the video's metadata
	dbVideo, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't find video", err)
		return
	}
	// Authorize user as video owner
	if dbVideo.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Not authorized to update this video", err)
		return
	}

	// Upload
	const uploadLimit = 1 << 30 // Set to 1GB
	r.Body = http.MaxBytesReader(w, r.Body, uploadLimit)

	// "thumbnail" should match the HTML form input name
	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	// Get the media type from the form file's Content-Type header
	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type", err)
		return
	}
	// Validate the uploaded file to ensure it's an MP4 video
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Invalid file type, only MP4 is allowed", nil)
		return
	}

	// Create temp empty system file on which to write the unprocessed video
	tempFileUnprocessed, err := os.CreateTemp(cfg.assetsRoot, "tubely-upload-*.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Could not create temp file", err)
		return
	}
	defer os.Remove(tempFileUnprocessed.Name())
	defer tempFileUnprocessed.Close()

	// Copy contents from multipart file to temp empty system file
	if _, err = io.Copy(tempFileUnprocessed, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Could not write file to disk", err)
		return
	}

	// Create a processed version of the video
	filePath, err := processVideoForFastStart(tempFileUnprocessed.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error processing video for fast start", err)
		return
	}
	tempFile, err := os.Open(filePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error opening file for processed video", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	// Get the aspect ratio of the video file
	directory := ""
	aspectRatio, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error determining aspect ratio", err)
		return
	}
	switch aspectRatio {
	case "16:9":
		directory = "landscape"
	case "9:16":
		directory = "portrait"
	default:
		directory = "other"
	}

	// Reset the tempFile's file pointer to the beginning to allow us to read the file again from the beginning
	// _, err = tempFile.Seek(0, io.SeekStart)
	// if err != nil {
	// 	respondWithError(w, http.StatusInternalServerError, "Could not reset file pointer", err)
	// 	return
	// }

	// Put the object into S3
	key := getAssetPath(mediaType)
	key = path.Join(directory, key)
	putObjectInput := s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(key), // The file name using <random-32-byte-hex>.ext format
		Body:        tempFile,
		ContentType: aws.String(mediaType),
	}
	_, err = cfg.s3Client.PutObject(r.Context(), &putObjectInput)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error uploading file to S3", err)
		return
	}

	// Update the VideoURL of the video record in the database with the S3 bucket and key.
	videoURL := cfg.getObjectURL(key)
	dbVideo.VideoURL = &videoURL
	err = cfg.db.UpdateVideo(dbVideo)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, dbVideo)
}

func getVideoAspectRatio(filePath string) (string, error) {
	// Get "streams" video info
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ffprobe error: %v", err)
	}
	// Unmarshal the stdout of the command into a JSON struct
	var output struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}
	err = json.Unmarshal(out, &output)
	if err != nil {
		return "", fmt.Errorf("could not parse ffprobe output: %v", err)
	}
	// The ffprobe can return an empty array of streams
	if len(output.Streams) == 0 {
		return "", errors.New("no video streams found")
	}

	// Determine video's aspect ratio
	width := float64(output.Streams[0].Width)
	height := float64(output.Streams[0].Height)
	// 9 / 16 = 0.562962963
	// 16 / 9 = 1.7777777778
	ratio := math.Floor((width/height)*100) / 100
	if ratio > 0.54 && ratio < 0.58 {
		return "9:16", nil
	} else if ratio > 1.74 && ratio < 1.78 {
		return "16:9", nil
	}
	return "other", nil
}

func processVideoForFastStart(filePath string) (string, error) {
	outputFilePath := filePath + ".processing"
	// Process filePath video
	cmd := exec.Command("ffmpeg", "-y", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outputFilePath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ffmpeg error: %v, stderr: %s", err, stderr.String())
	}
	return outputFilePath, nil
}
