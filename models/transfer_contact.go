package models

import "time"

type TransferContact struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	SenderID   uint      `gorm:"not null;index:idx_sender_receiver,unique" json:"sender_id"`
	ReceiverID uint      `gorm:"not null;index:idx_sender_receiver,unique" json:"receiver_id"`
	UpdatedAt  time.Time `gorm:"not null" json:"-"`
}

func (TransferContact) TableName() string {
	return "transfer_contacts"
}
