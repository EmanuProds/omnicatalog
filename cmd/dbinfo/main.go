package main

import (
	"fmt"
	"omnicatalog/internal/config"
	"omnicatalog/internal/store"
)

func main() {
	cfg, _ := config.Load()
	s, _ := store.NewStore(cfg.Database.Path, cfg.Database.MigrationDir)
	defer s.Close()
	p, _ := s.GetSetting("selected_providers")
	m, _ := s.GetSetting("selected_models")
	fmt.Printf("providers=%s\nmodels=%s\n", p, m)
}
