package ui

import (
	"context"
	"time"

	"timberlake/config"
	"timberlake/s3client"
	"timberlake/scanner"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type ProgramState int

const (
	StateScanning ProgramState = iota
	StateUploading
	StatePaused
	StateVerification
	StateDone
	StateError
)

type WorkerState struct {
	ID           int
	Status       string // "Idle", "Checking", "Uploading", "Done", "Skipped", "Error"
	FileName     string
	AbsolutePath string
	RelativePath string
	TotalSize    int64
	UploadedSize int64
	SpeedBps     float64
	LastError    string
	StartTime    time.Time
}

type Model struct {
	Config       *config.AppConfig
	S3Client     *s3client.S3Client
	ScanResult   *scanner.ScanResult
	State        ProgramState
	ErrorMessage string

	// Stats
	TotalBytes    int64
	UploadedBytes int64
	SkippedBytes  int64
	TotalFiles    int64
	UploadedFiles int64
	SkippedFiles  int64
	FailedFiles   int64

	// Workers & Message Channel
	Workers   []WorkerState
	WorkQueue []scanner.FileItem
	MsgChan   chan tea.Msg

	// Timing & Speed
	StartTime  time.Time
	EndTime    time.Time
	LastUpdate time.Time
	SpeedBps   float64

	// TUI Components
	TotalBytesBar progress.Model
	TotalFilesBar progress.Model
	WorkerBars    []progress.Model
	Spinner       spinner.Model
	Viewport      viewport.Model

	// Trivia
	TriviaList       []string
	TriviaIndex      int
	LastTriviaUpdate time.Time

	// Dimensions
	Width  int
	Height int

	// Context & Channels
	Ctx    context.Context
	Cancel context.CancelFunc
}

var NSyncTrivia = []string{
	"The name 'N SYNC came from the last letter of each original member's first name: JustiN, ChriS, JoeY, JasoN, and JC!",
	"When original member Jason Galasso left, Lance Bass joined. Justin's mom nicknamed Lance 'Lansten' so the 'N' in 'N SYNC would still work!",
	"The star in *NSYNC was suggested by illusionist Uri Geller, who predicted the band would have immense fortune connected to stars.",
	"In 2002, Lance Bass trained at Star City in Russia and became a certified cosmonaut aiming to fly to the International Space Station!",
	"Both Chris Kirkpatrick and Joey Fatone worked at Universal Studios Florida before 'N SYNC. Joey played Wolfman in the Beetlejuice show!",
	"Chris Kirkpatrick voiced iconic cartoon pop star Chip Skylark on Nickelodeon's 'The Fairly OddParents', singing 'My Shiny Teeth and Me'!",
	"In 2000, 'No Strings Attached' sold 1.1 million copies in its first DAY and 2.4 million in week one—a record unbroken for 15 years!",
	"Justin Timberlake and JC Chasez both got their big break on Disney's 'The All-New Mickey Mouse Club' alongside Britney Spears and Ryan Gosling.",
	"Before taking America by storm, 'N SYNC launched their career in Germany, releasing their debut single 'I Want You Back' there in 1996!",
	"Joey Fatone is a passionate foodie and founded his own hot dog business called 'Fat One's' with a food truck in Orlando, Florida!",
	"At age 11, Justin Timberlake competed as a country singer named 'Justin Randall' on Star Search wearing a cowboy hat!",
	"Joey Fatone competed on Season 1 of 'The Masked Singer' disguised as 'The Rabbit' and finished in 3rd place!",
	"Justin Timberlake's mother served as legal guardian for Ryan Gosling for 6 months so he could stay in Orlando to film Mickey Mouse Club.",
	"JC Chasez has written and produced songs for numerous artists including the Backstreet Boys and served 7 seasons as a judge on America's Best Dance Crew.",
	"In the 'Drive Myself Crazy' video, Joey Fatone spent the shoot in a straightjacket and a Superman suit in a padded room.",
	"In 2023, 'N SYNC reunited to release 'Better Place' for the film 'Trolls Band Together'—their first new song in over 20 years!",
	"'N SYNC is one of the elite few musical acts in history to achieve TWO RIAA Diamond Certified albums ('*NSYNC' and 'No Strings Attached').",
	"The band has sold over 70 million records worldwide, cementing their status as one of the best-selling boy bands of all time.",
	"'N SYNC co-headlined the legendary Super Bowl XXXV Halftime Show in 2001 alongside Aerosmith, Britney Spears, Nelly, and Mary J. Blige.",
	"In 2018, all five members reunited on Hollywood Boulevard to receive their star on the Hollywood Walk of Fame.",
	"Justin Timberlake famously wore an all-denim tuxedo matching Britney Spears at the 2001 American Music Awards.",
	"JC Chasez was adopted at age 5 by his foster parents, Roy and Karen Chasez, who encouraged his passion for music and dance.",
}

// Messages
type ScanProgressMsg struct {
	ScannedFiles int64
	ScannedBytes int64
}

type ScanCompleteMsg struct {
	Result *scanner.ScanResult
	Err    error
}

type WorkerProgressMsg struct {
	WorkerID     int
	BytesDelta   int64
	UploadedSize int64
	TotalSize    int64
	FileName     string
}

type WorkerStatusMsg struct {
	WorkerID int
	Status   string // "Checking", "Uploading", "Done", "Skipped", "Error"
	FileName string
	Err      string
	Size     int64
}

type VerificationCompleteMsg struct {
	Passed bool
	ErrMsg string
}

func InitialModel(appCfg *config.AppConfig) Model {
	ctx, cancel := context.WithCancel(context.Background())

	// Gradient progress bar for overall data
	bytesBar := progress.New(
		progress.WithDefaultGradient(),
		progress.WithoutPercentage(),
	)

	// Solid blue progress bar for total files count
	filesBar := progress.New(
		progress.WithSolidFill("#00BFFF"),
		progress.WithoutPercentage(),
	)

	workerBars := make([]progress.Model, appCfg.Jobs)
	workers := make([]WorkerState, appCfg.Jobs)
	for i := 0; i < appCfg.Jobs; i++ {
		workerBars[i] = progress.New(
			progress.WithSolidFill("#7D56F4"),
			progress.WithoutPercentage(),
		)
		workers[i] = WorkerState{
			ID:     i,
			Status: "Idle",
		}
	}

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#6944BA"))

	vp := viewport.New(80, 10)

	return Model{
		Config:           appCfg,
		State:            StateScanning,
		Workers:          workers,
		MsgChan:          make(chan tea.Msg, 1000),
		TotalBytesBar:    bytesBar,
		TotalFilesBar:    filesBar,
		WorkerBars:       workerBars,
		Spinner:          sp,
		Viewport:         vp,
		TriviaList:       NSyncTrivia,
		TriviaIndex:      0,
		LastTriviaUpdate: time.Now(),
		Ctx:              ctx,
		Cancel:           cancel,
		StartTime:        time.Now(),
		LastUpdate:       time.Now(),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.Spinner.Tick,
		startScanCmd(m.Config.SourceDir),
		waitForMsgCmd(m.MsgChan),
	)
}

func startScanCmd(sourceDir string) tea.Cmd {
	return func() tea.Msg {
		res, err := scanner.ScanDirectory(sourceDir, nil)
		return ScanCompleteMsg{
			Result: res,
			Err:    err,
		}
	}
}

func waitForMsgCmd(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}
