package models

type Bank struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Name      string `gorm:"size:100;not null" json:"name"`
	ShortName string `gorm:"size:20" json:"short_name"`
	Type      string `gorm:"type:enum('bank','ewallet');default:'bank'" json:"type"`
	Code      string `gorm:"size:20;uniqueIndex;not null" json:"code"` // gateway code: 014 (BCA), DANA (ewallet)
	Status    string `gorm:"type:enum('Active','Inactive');default:'Active'" json:"status"`
}

func (Bank) TableName() string {
	return "banks"
}
