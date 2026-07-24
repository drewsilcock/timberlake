package ui

import (
	"context"
	"time"

	"timberlake/config"
	"timberlake/transfer"

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
	StateCancelled
)

// FileError records a single failed upload for the on-exit error log.
type FileError struct {
	RelativePath string
	Message      string
}

// speedSample is one point in the rolling upload-throughput window.
type speedSample struct {
	at    time.Time
	bytes int64
}

type WorkerState struct {
	ID            int
	Status        string // "Idle", "Checking", "Uploading", "Done", "Skipped", "Error"
	FileName      string
	AbsolutePath  string
	RelativePath  string
	TotalSize     int64
	CommittedSize int64 // bytes in parts acknowledged by the server (finished chunks)
	UploadedSize  int64 // bytes streamed to the wire, incl. the part(s) in flight
	BufferedSize  int64 // bytes read from disk into buffers (>= UploadedSize)
	SpeedBps      float64
	LastError     string
	StartTime     time.Time
}

type Model struct {
	Config       *config.AppConfig
	Source       transfer.Source
	Dest         transfer.Destination
	State        ProgramState
	ErrorMessage string

	// Stats
	TotalBytes     int64
	UploadedBytes  int64 // derived: fully-completed files + in-flight worker progress
	CompletedBytes int64 // bytes from files that finished uploading
	SkippedBytes   int64
	TotalFiles     int64
	UploadedFiles  int64
	SkippedFiles   int64
	FailedFiles    int64

	// Workers & Message Channel
	Workers   []WorkerState
	WorkQueue []transfer.Item
	MsgChan   chan tea.Msg

	// Timing & Speed
	StartTime  time.Time
	EndTime    time.Time
	LastUpdate time.Time
	SpeedBps   float64

	// Rolling-window upload-speed sampling (see sampleSpeed).
	speedSamples  []speedSample
	lastSpeedTime time.Time

	// Errors collected from failed uploads, written to a log on exit.
	Errors []FileError

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
	"'It's Gonna Be Me' became 'N SYNC's only Billboard Hot 100 number-one single in the United States, topping the chart in August 2000.",
	"Because of Justin's pronunciation, 'It's Gonna Be Me' spawned the beloved 'It's Gonna Be May' meme every 30th of April.",
	"'No Strings Attached' was named partly as a jab at their old label — the cover shows the members dangling as marionettes cutting their strings.",
	"The group's debut US single 'I Want You Back' should not be confused with the Jackson 5 hit of the same name.",
	"'N SYNC's third album 'Celebrity' (2001) featured a heavier songwriting hand from Justin Timberlake and JC Chasez.",
	"'Bye Bye Bye' was famously performed with marionette-style choreography in its iconic music video.",
	"'N SYNC's manager Lou Pearlman also created the Backstreet Boys, fueling a fierce boy-band rivalry in the late '90s.",
	"The members later sued Lou Pearlman over unfair contracts; Pearlman was eventually convicted in a massive Ponzi scheme.",
	"JC Chasez released his solo debut album 'Schizophrenic' in 2004 after 'N SYNC went on hiatus.",
	"Justin Timberlake's 2002 solo debut 'Justified' launched one of the most successful careers to ever come out of a boy band.",
	"Joey Fatone hosted the TV game/music shows 'The Singing Bee' and 'Common Sense' after 'N SYNC.",
	"Lance Bass came out publicly in 2006 and has since been a prominent LGBTQ+ advocate.",
	"'N SYNC headlined the 'PopOdyssey' and 'Celebrity' stadium tours in 2001–2002.",
	"Michael Jackson joined 'N SYNC on stage at his 30th Anniversary Celebration concert in 2001.",
	"Justin Timberlake showed off beatboxing skills with 'N SYNC that he'd carry into his solo career.",
	"'Merry Christmas, Happy Holidays' from 1998's 'Home for Christmas' remains a seasonal radio staple.",
	"'N SYNC's 'Girlfriend' (2002) got a remix featuring Nelly, one of their final singles before the hiatus.",
	"JC Chasez and Justin Timberlake traded lead vocals across most of 'N SYNC's biggest hits.",
	"'N SYNC won multiple MTV Video Music Awards during their late-'90s and early-2000s peak.",
	"Chris Kirkpatrick is credited with first assembling the group in Orlando in 1995.",
	"'No Strings Attached' set a first-week US sales record, surpassing the mark set by the Backstreet Boys.",
	"Joey Fatone appeared on Broadway, including a run in 'Rent'.",
	"The 2001 film 'On the Line' starred 'N SYNC's Lance Bass and Joey Fatone.",
	"'N SYNC's 'Pop' featured groundbreaking Wade Robson choreography and an award-winning music video.",
	"Lance Bass has worked as a producer and radio/TV host since 'N SYNC's hiatus.",
	"After 2002 the members went on an indefinite hiatus rather than a formal breakup.",
	"'N SYNC's a cappella talents were on full display in live medleys that showed they could really sing without backing tracks.",
	"The 2013 MTV VMAs featured a brief 'N SYNC reunion during Justin Timberlake's Video Vanguard Award performance.",
}

// Messages
type ScanProgressMsg struct {
	ScannedFiles int64
	ScannedBytes int64
}

type ScanCompleteMsg struct {
	Items      []transfer.Item
	TotalBytes int64
	Err        error
}

type WorkerProgressMsg struct {
	WorkerID      int
	CommittedSize int64 // absolute bytes in acknowledged parts
	UploadedSize  int64 // absolute bytes streamed to the wire (incl. in-flight part)
	BufferedSize  int64 // absolute bytes read from disk into buffers
	TotalSize     int64
	FileName      string
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

func InitialModel(appCfg *config.AppConfig, source transfer.Source, dest transfer.Destination) Model {
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
		Source:           source,
		Dest:             dest,
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
		startScanCmd(m.Ctx, m.Source),
		waitForMsgCmd(m.MsgChan),
	)
}

func startScanCmd(ctx context.Context, src transfer.Source) tea.Cmd {
	return func() tea.Msg {
		items, err := src.Scan(ctx, nil)
		var total int64
		for _, it := range items {
			total += it.Size
		}
		return ScanCompleteMsg{Items: items, TotalBytes: total, Err: err}
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
