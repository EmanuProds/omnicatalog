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
	ids := []string{"nvidia/nvidia/nemotron-3-ultra-550b-a55b","nvidia/glm-5.3-flash","nvidia/nemotron-3.5-super-vl-120b-a12b"}
	for _, id := range ids {
		m, _ := s.GetModel(id)
		if m == nil { fmt.Println(id,"MISSING"); continue }
		fmt.Printf("%s free=%v confirmed=%v context=%v tools=%v\n", id, m.IsFree, m.FreeConfirmed, m.ContextWindow, m.SupportsTools)
		b, _ := s.GetBenchmark(id)
		fmt.Printf("   benchmark=%+v\n", b)
		v, _ := s.GetLatestValidation(id, "free")
		fmt.Printf("   free val=%+v\n", v)
	}
}
