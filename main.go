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
	"strings"
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

	dryRunFlag              = flag.Bool("dry-run", false, "dry-run mode")
	sfTokenFlag             = flag.String("secureframe-token", "", "Secureframe bearer token")
	companyIDFlag           = flag.String("company-id", "adcfb3c-0b58-4c2c-af04-43b1a5031d61", "secureframe company ID")
	companyUserIDFlag       = flag.String("company-user-id", "079b854c-c53a-4c71-bfb8-f9e87b13b6c4", "secureframe company user ID")
	employeeTypesFlag       = flag.String("employee-types", "employee,contractor", "types of employees to contact")
	robotNameFlag           = flag.String("robot-name", "ComplyBot3000", "name of the robot")
	companyNameFlag         = flag.String("company-name", "Chainguard", "The name of your compnay")
	securityTrainingURLFlag = flag.String("security-training-url", "https://securityawareness.usalearning.gov/cybersecurity/index.htm", "URL to security training")
	helpChannelFlag         = flag.String("help-channel", "#security-and-compliance", "Slack channel for help")
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

func nag(s *slack.Client, email string, needs []string) error {
	if s == nil {
		log.Printf("would nag %s about %s, but no Slack client was setup.", email, needs)
	}
	u, err := s.GetUserByEmail(email)
	if err != nil {
		return fmt.Errorf("get user by email: %w", err)
	}
	log.Printf("found user: %+v", u)
	text, err := messageText(MessageContext{
		Needs:               needs,
		FirstName:           u.Profile.FirstName,
		HelpChannel:         *helpChannelFlag,
		Company:             *companyNameFlag,
		SecurityTrainingURL: *securityTrainingURLFlag,
		BotName:             *robotNameFlag,
	})
	if err != nil {
		return fmt.Errorf("message text: %w", err)
	}

	log.Printf("message to %s: %s", email, text)
	if !*dryRunFlag {
		_, _, err := s.PostMessage(u.ID, slack.MsgOptionText(text, false))
		if err != nil {
			return fmt.Errorf("post message: %w", err)
		}
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

	ppl, err := secureframe.Personnel(context.Background(), *companyIDFlag, *companyUserIDFlag, *sfTokenFlag)
	if err != nil {
		log.Panicf("Secureframe test query failed: %v", err)
	}
	log.Printf("PPL: -- %+v -- ", ppl)

	requiredTypes := map[string]bool{}
	for _, t := range strings.Split(*employeeTypesFlag, ",") {
		requiredTypes[strings.ToLower(t)] = true
	}

	for _, p := range ppl {
		if !p.Active {
			continue
		}
		if !p.Invited {
			continue
		}
		if !p.InAuditScope {
			continue
		}

		eType := strings.ToLower(p.EmployeeType)
		if !requiredTypes[eType] {
			continue
		}

		needs := []string{}
		if !p.PoliciesAccepted {
			needs = append(needs, "Accept or re-accept our company policies at https://app.secureframe.com/onboard/employee/policies")
		}
		if !p.SecurityTrainingCompleted {
			needs = append(needs, "Take the Cybersecurity Awareness Training at {{.SecurityTrainingURL}}")
			needs = append(needs, "Upload proof of training completion to https://app.secureframe.com/onboard/employee/training (PDF or screenshot)")
		}

		if len(needs) > 0 {
			log.Printf("%s needs %s", p.Email, needs)

			email := p.Email
			if *testMessageTarget != "" {
				email = *testMessageTarget
			}
			if err := nag(s, email, needs); err != nil {
				log.Printf("failed to nag %s: %v", p.Email, err)
			}
			if *testMessageTarget != "" {
				log.Printf("sent test message, exiting")
				break
			}

			time.Sleep(250 * time.Millisecond)
		}
	}
}
