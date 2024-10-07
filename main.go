package main

import (
	"bytes"
	"context"
	_ "embed"
	"flag"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"os"
	"time"

	"github.com/chainguard-dev/secureframe-policy-minder/pkg/secureframe"
	"github.com/danott/envflag"
	"github.com/slack-go/slack"
)

var (
	greetings = []string{
		"Greetings and salutations",
		"Ahoy-hoy",
		"Konnichiwa",
		"Buongiorno",
		"Hola",
		"Habari",
		"Goedendag",
		"Namaste",
		"Shalom",
	}

	accessKeyFlag           = flag.String("access-key", "", "Secureframe Access Key")
	companyFlag             = flag.String("company", "", "Company name used for notifications")
	dryRunFlag              = flag.Bool("dry-run", false, "dry-run mode")
	employeeTypesFlag       = flag.String("employee-types", "employee,contractor", "types of employees to contact")
	helpChannelFlag         = flag.String("help-channel", "#security-and-compliance", "Slack channel for help")
	robotNameFlag           = flag.String("robot-name", "ComplyBot3000", "name of the robot")
	secretKeyFlag           = flag.String("secret-key", "", "Secureframe Secret Key")
	securityTrainingURLFlag = flag.String("security-training-url", "https://securityawareness.usalearning.gov/cybersecurity/index.htm", "URL to security training")
	testMessageTarget       = flag.String("test-message-target", "", "override destination and send a single test message to this person")

	//go:embed message.tmpl
	msgTmpl string
)

type MessageContext struct {
	Email               string
	BotName             string
	Greetings           string
	FirstName           string
	Company             string
	SecurityTrainingURL string
	Needs               []string
	InterpretedNeeds    []string
	HelpChannel         string
}

func messageText(m MessageContext) (string, error) {
	if m.Greetings == "" {
		gn := rand.Intn(len(greetings))
		m.Greetings = greetings[gn]
	}

	// Treat needs as a series of mini templates
	for _, n := range m.Needs {
		tmpl, err := template.New("need").Parse(n)
		if err != nil {
			return "", fmt.Errorf("parse: %v", err)
		}

		var tpl bytes.Buffer
		if err = tmpl.Execute(&tpl, m); err != nil {
			return "", fmt.Errorf("exec: %w", err)
		}

		m.InterpretedNeeds = append(m.InterpretedNeeds, tpl.String())
	}

	tmpl, err := template.New("msg").Parse(msgTmpl)
	if err != nil {
		return "", fmt.Errorf("parse: %v", err)
	}

	var tpl bytes.Buffer
	if err = tmpl.Execute(&tpl, m); err != nil {
		return "", fmt.Errorf("exec: %w", err)
	}

	return tpl.String(), nil
}

func remind(s *slack.Client, company string, email string, needs []string) error {
	firstName := "Unknown"
	uid := "unknown"

	if s == nil {
		log.Printf("would nag %s about %s, but no Slack client was setup.", email, needs)
	} else {
		u, err := s.GetUserByEmail(email)
		if err != nil {
			return fmt.Errorf("get user by email: %w", err)
		}
		log.Printf("found user: %+v", u)
		firstName = u.Profile.FirstName
		uid = u.ID
	}

	text, err := messageText(MessageContext{
		Needs:               needs,
		FirstName:           firstName,
		HelpChannel:         *helpChannelFlag,
		Company:             company,
		SecurityTrainingURL: *securityTrainingURLFlag,
		BotName:             *robotNameFlag,
	})
	if err != nil {
		return fmt.Errorf("message text: %w", err)
	}

	if !*dryRunFlag {
		log.Printf("posting message to %s: %s", email, text)
		_, _, err := s.PostMessage(uid, slack.MsgOptionText(text, false))
		if err != nil {
			return fmt.Errorf("post message: %w", err)
		}
	} else {
		log.Printf("DRY-RUN for %s: %s", email, text)
	}
	return nil
}

func main() {
	flag.Parse()
	// makes SECUREFRAME_TOKEN available to secureframe-token
	envflag.Parse()

	var s *slack.Client

	token := os.Getenv("SLACK_TOKEN")
	if token != "" {
		log.Printf("setting up slack client (%d byte token)", len(token))
		s = slack.New(token)
	} else {
		log.Printf("SLACK_TOKEN not set, won't actually post messages to Slack")
	}

	ctx := context.Background()

	ppl, err := secureframe.Users(ctx, *accessKeyFlag, *secretKeyFlag, *employeeTypesFlag)
	if err != nil {
		log.Panicf("Failed to get Secureframe users: %v", err)
	}
	log.Printf("PPL: -- %+v -- ", ppl)

	// Provide a date string for three days in the future
	threeDays := time.Now().AddDate(0, 0, 3).Format(time.RFC850)

	for _, info := range ppl {
		// Instructions to be populated based on the user's onboarding status
		needs := []string{}

		// Account for users with training that has not started or with outstanding security training
		switch info["onboarding_status"] {
		case "not_started":
			needs = append(needs, `✅ Accept our latest policies at https://app.secureframe.com/onboard/employee/policies`)
			needs = append(needs, `🏋️‍♀️ Take Cybersecurity training at {{.SecurityTrainingURL}}`)
			needs = append(needs, `⬆️ Upload proof of completion to https://app.secureframe.com/onboard/employee/training (PDF or screenshot)`)
			needs = append(needs, fmt.Sprintf(`📆 Accept policies and upload proof within 72 hours of receiving this notification (by %s)`, threeDays))
		case "security_training":
			needs = append(needs, `🏋️‍♀️ Take Cybersecurity training at {{.SecurityTrainingURL}}`)
			needs = append(needs, `⬆️ Upload proof of completion to https://app.secureframe.com/onboard/employee/training (PDF or screenshot)`)
			needs = append(needs, fmt.Sprintf(`📆 Upload proof within 72 hours of receiving this notification (by %s)`, threeDays))
		default:
			continue
		}

		if len(needs) > 0 {
			email := info["email"]
			log.Printf("%s needs %s", email, needs)

			if *testMessageTarget != "" {
				email = *testMessageTarget
			}
			if err := remind(s, *companyFlag, email, needs); err != nil {
				log.Printf("failed to nag %s: %v", email, err)
			}
			if *testMessageTarget != "" {
				log.Printf("sent test message, exiting")
				break
			}

			time.Sleep(250 * time.Millisecond)
		}
	}
}
