package usecase

import (
	"context"
	"errors"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/support/domain"
	"strings"
	"testing"
)

const author = "4f9a4c95-6144-4ec8-89e8-3866207d7561"
const target = "00000000-0000-4000-8000-000000000002"
const file = "00000000-0000-4000-8000-000000000003"

func ptr(s string) *string { return &s }
func validInput() domain.NewComplaint {
	return domain.NewComplaint{UserID: author, Subject: " Жалоба ", Description: " Описание ", TargetUserID: ptr(target)}
}

type fakeRepo struct {
	calls int
	input domain.NewComplaint
	err   error
}

func (r *fakeRepo) Create(_ context.Context, input domain.NewComplaint) (domain.Complaint, error) {
	r.calls++
	r.input = input
	return domain.Complaint{ID: "created", Status: "open"}, r.err
}

type fakeMedia struct {
	calls          int
	userID, fileID string
	err            error
}

func (m *fakeMedia) ValidateAttachment(_ context.Context, userID, fileID string) error {
	m.calls++
	m.userID = userID
	m.fileID = fileID
	return m.err
}

func TestCreateComplaintValidation(t *testing.T) {
	cases := map[string]func(*domain.NewComplaint){
		"missing author":    func(i *domain.NewComplaint) { i.UserID = "" },
		"nil UUID":          func(i *domain.NewComplaint) { i.UserID = "00000000-0000-0000-0000-000000000000" },
		"missing target":    func(i *domain.NewComplaint) { i.TargetUserID = nil },
		"both targets":      func(i *domain.NewComplaint) { i.TargetProjectID = ptr(target) },
		"empty target":      func(i *domain.NewComplaint) { i.TargetUserID = ptr("") },
		"bad project":       func(i *domain.NewComplaint) { i.TargetUserID = nil; i.TargetProjectID = ptr("bad") },
		"blank subject":     func(i *domain.NewComplaint) { i.Subject = " \t" },
		"long subject":      func(i *domain.NewComplaint) { i.Subject = strings.Repeat("я", 256) },
		"blank description": func(i *domain.NewComplaint) { i.Description = "\n" },
		"long description":  func(i *domain.NewComplaint) { i.Description = strings.Repeat("я", MaxDescriptionLength+1) },
		"NUL description":   func(i *domain.NewComplaint) { i.Description = "a\x00b" },
		"invalid UTF8":      func(i *domain.NewComplaint) { i.Subject = string([]byte{255}) },
		"bad file":          func(i *domain.NewComplaint) { i.FileIDs = []string{"bad"} },
		"duplicate file":    func(i *domain.NewComplaint) { i.FileIDs = []string{file, strings.ReplaceAll(file, "-", "")} },
		"too many files":    func(i *domain.NewComplaint) { i.FileIDs = make([]string, MaxAttachments+1) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			input := validInput()
			change(&input)
			repo := &fakeRepo{}
			media := &fakeMedia{}
			_, err := NewService(repo, media).CreateComplaint(context.Background(), input)
			if !errors.Is(err, domain.ErrInvalidArgument) || repo.calls != 0 || media.calls != 0 {
				t.Fatalf("invalid input reached dependencies: %v %+v %+v", err, repo, media)
			}
		})
	}
}
func TestCreateComplaintTargetsAndAttachments(t *testing.T) {
	for _, project := range []bool{false, true} {
		input := validInput()
		if project {
			input.TargetUserID = nil
			input.TargetProjectID = ptr(target)
		}
		repo := &fakeRepo{}
		media := &fakeMedia{}
		result, err := NewService(repo, media).CreateComplaint(context.Background(), input)
		if err != nil || result.Status != "open" || repo.calls != 1 || media.calls != 0 || repo.input.Subject != "Жалоба" || repo.input.Description != "Описание" {
			t.Fatalf("without files: %v %+v", err, repo)
		}
	}
	input := validInput()
	input.FileIDs = []string{file}
	repo := &fakeRepo{}
	media := &fakeMedia{}
	_, err := NewService(repo, media).CreateComplaint(context.Background(), input)
	if err != nil || repo.calls != 1 || media.calls != 1 || media.userID != author || media.fileID != file {
		t.Fatal("attachment not checked", err)
	}
	for _, failure := range []error{domain.ErrAttachmentNotFound, domain.ErrAttachmentNotReady, domain.ErrMediaUnavailable, context.Canceled} {
		repo := &fakeRepo{}
		media := &fakeMedia{err: failure}
		_, err := NewService(repo, media).CreateComplaint(context.Background(), input)
		if !errors.Is(err, failure) || repo.calls != 0 {
			t.Fatalf("failed attachment persisted: %v", err)
		}
	}
	repo = &fakeRepo{err: errors.New("database unavailable")}
	_, err = NewService(repo, &fakeMedia{}).CreateComplaint(context.Background(), validInput())
	if !errors.Is(err, repo.err) {
		t.Fatal("repository error lost", err)
	}
}
