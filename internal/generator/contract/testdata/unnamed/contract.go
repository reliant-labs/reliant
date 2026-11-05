package unnamed

import "context"

// Service defines the unnamed package boundary.
type Service interface {
	CreatePatient(context.Context, CreatePatientInput) (PatientProfile, error)
	GetPatient(context.Context, string) (PatientProfile, error)
	ListAll() ([]PatientProfile, error)
}

type CreatePatientInput struct {
	Name string `json:"name"`
}

type PatientProfile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
