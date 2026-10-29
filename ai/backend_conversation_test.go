package ai

import (
	"errors"
	"testing"
)

type conversationConfigTestDriver struct {
	LMDriver
	constrainedDecoding []bool
	setterErr           error
	conversationCreates int
}

func (*conversationConfigTestDriver) stringToCPtr(string) (uint64, func(), error) {
	return 1, func() {}, nil
}

func (*conversationConfigTestDriver) cfgCreate(uint64) uint64            { return 1 }
func (*conversationConfigTestDriver) cfgDelete(uint64)                   {}
func (*conversationConfigTestDriver) cfgSetTools(uint64, uint64)         {}
func (*conversationConfigTestDriver) cfgSetSystemMessage(uint64, uint64) {}
func (d *conversationConfigTestDriver) cfgSetEnableConstrainedDecoding(_ uint64, enable bool) error {
	d.constrainedDecoding = append(d.constrainedDecoding, enable)
	return d.setterErr
}
func (*conversationConfigTestDriver) cfgSetStreamToolCalls(uint64, bool, uint64) {}
func (*conversationConfigTestDriver) cfgSetMessages(uint64, uint64)              {}
func (d *conversationConfigTestDriver) convCreate(uint64, uint64) uint64 {
	d.conversationCreates++
	return 2
}
func (*conversationConfigTestDriver) convDelete(uint64) {}

func TestNewConversationEnablesConstrainedDecodingWhenToolsArePresent(t *testing.T) {
	tests := []struct {
		name       string
		config     ConversationConfig
		wantEnable bool
	}{
		{
			name: "with tools",
			config: ConversationConfig{
				Tools: []map[string]any{{"type": "function"}},
			},
			wantEnable: true,
		},
		{
			name:       "without tools",
			config:     ConversationConfig{},
			wantEnable: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			driver := &conversationConfigTestDriver{}
			backend := &drvEngineBackend{drv: driver, engine: 1}

			conv, err := backend.NewConversation(test.config)
			if err != nil {
				t.Fatalf("NewConversation() error = %v", err)
			}
			defer conv.Close()

			if len(driver.constrainedDecoding) != 1 || driver.constrainedDecoding[0] != test.wantEnable {
				t.Fatalf("constrained-decoding settings = %v, want [%t]", driver.constrainedDecoding, test.wantEnable)
			}
			if driver.conversationCreates != 1 {
				t.Fatalf("conversation creates = %d, want 1", driver.conversationCreates)
			}
		})
	}
}

func TestNewConversationReturnsConstrainedDecodingConfigurationError(t *testing.T) {
	setterErr := errors.New("setter failed")
	driver := &conversationConfigTestDriver{setterErr: setterErr}
	backend := &drvEngineBackend{drv: driver, engine: 1}

	conv, err := backend.NewConversation(ConversationConfig{
		Tools: []map[string]any{{"type": "function"}},
	})
	if err == nil || !errors.Is(err, setterErr) {
		t.Fatalf("NewConversation() error = %v, want wrapped setter error", err)
	}
	if conv != nil {
		t.Fatal("NewConversation() returned a conversation after configuration failed")
	}
	if driver.conversationCreates != 0 {
		t.Fatalf("conversation creates = %d, want 0 after configuration failure", driver.conversationCreates)
	}
}
