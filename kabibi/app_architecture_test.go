package main

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAppHostIsTheTeaModelBoundary(t *testing.T) {
	hostType := reflect.TypeOf(&AppHost{})
	modelType := reflect.TypeOf((*tea.Model)(nil)).Elem()
	if !hostType.Implements(modelType) {
		t.Fatal("AppHost does not implement tea.Model")
	}

	widgetType := reflect.TypeOf(&AppWidget{})
	if widgetType.Implements(modelType) {
		t.Fatal("AppWidget should not implement tea.Model; AppHost is the Bubble Tea boundary")
	}
}
