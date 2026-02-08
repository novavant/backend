package models

import "time"

// Gift represents a gift/dana kaget that user creates to share with others
type Gift struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	UserID           uint      `gorm:"not null;index" json:"user_id"`
	Code             string    `gorm:"size:12;uniqueIndex;not null" json:"code"`
	Amount           float64   `gorm:"type:decimal(15,2);not null" json:"amount"` // total (random) or per-winner (equal)
	WinnerCount      int       `gorm:"not null" json:"winner_count"`
	DistributionType string    `gorm:"type:enum('random','equal');not null" json:"distribution_type"`
	RecipientType    string    `gorm:"type:enum('all','referral_only');not null" json:"recipient_type"`
	Status           string    `gorm:"type:enum('active','completed','expired','cancelled');default:'active'" json:"status"`
	TotalDeducted    float64   `gorm:"type:decimal(15,2);not null" json:"total_deducted"` // amount actually deducted from sender
	CreatedAt        time.Time `json:"created_at"`

	// Associations
	User   *User            `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Claims []GiftClaim      `gorm:"foreignKey:GiftID" json:"claims,omitempty"`
	Slots  []GiftAmountSlot `gorm:"foreignKey:GiftID" json:"slots,omitempty"`
}

func (Gift) TableName() string {
	return "gifts"
}

// GiftAmountSlot stores pre-allocated amounts for random distribution
type GiftAmountSlot struct {
	ID        uint    `gorm:"primaryKey" json:"id"`
	GiftID    uint    `gorm:"not null;index" json:"gift_id"`
	SlotIndex int     `gorm:"not null" json:"slot_index"`
	Amount    float64 `gorm:"type:decimal(15,2);not null" json:"amount"`
}

func (GiftAmountSlot) TableName() string {
	return "gift_amount_slots"
}

// GiftClaim represents a user claiming/redeeming a gift
type GiftClaim struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	GiftID    uint      `gorm:"not null;index" json:"gift_id"`
	UserID    uint      `gorm:"not null;index" json:"user_id"`
	Amount    float64   `gorm:"type:decimal(15,2);not null" json:"amount"`
	SlotIndex int       `gorm:"not null;default:0" json:"slot_index"`
	CreatedAt time.Time `json:"created_at"`

	// Associations
	Gift *Gift `gorm:"foreignKey:GiftID" json:"gift,omitempty"`
	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (GiftClaim) TableName() string {
	return "gift_claims"
}
