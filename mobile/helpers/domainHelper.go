package helper

import (
	"mobileapi/database"
)

func ValidateDomain(domain string) bool {
	mongoDB, ok := database.DB.(*database.MongoDB)
	if !ok {
		return false
	}
	return mongoDB.IsDomainValid(domain)
}
