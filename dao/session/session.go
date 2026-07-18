package session

import (
	"GopherAI/common/postgres"
	"GopherAI/model"
)

func GetSessionsByUserName(UserName int64) ([]model.Session, error) {
	var sessions []model.Session
	err := postgres.DB.Where("user_name = ?", UserName).Find(&sessions).Error
	return sessions, err
}

func CreateSession(session *model.Session) (*model.Session, error) {
	err := postgres.DB.Create(session).Error
	return session, err
}

func GetSessionByID(sessionID string) (*model.Session, error) {
	var session model.Session
	err := postgres.DB.Where("id = ?", sessionID).First(&session).Error
	return &session, err
}
