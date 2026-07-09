package service

import (
	"database/sql"
	"encoding/hex"
	"log/slog"
	"sort"

	"hirebridge/internal/store/repo"
)

type SearchService struct {
	DB       *sql.DB
	Logger   *slog.Logger
	EmbedDim int
}

func NewSearchService(db *sql.DB, logger *slog.Logger, embedDim int) *SearchService {
	return &SearchService{DB: db, Logger: logger, EmbedDim: embedDim}
}

type TalentPointer struct {
	CandidateID string  `json:"candidate_id"`
	NodeID      string  `json:"node_id"`
	EndpointURL string  `json:"endpoint_url"`
	DisplayName string  `json:"display_name,omitempty"`
	Snippet     string  `json:"snippet,omitempty"`
	BM25Score   float64 `json:"bm25_score"`
	VecScore    float64 `json:"vec_score,omitempty"`
	Rank        int     `json:"rank"`
	LastSync    int64   `json:"last_sync"`
	IsActive    bool    `json:"is_active"`
}

type SearchResult struct {
	Candidates []TalentPointer `json:"candidates"`
}

func (s *SearchService) SearchTalent(query string, limit int, queryVector []float64) (*SearchResult, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	bm25Hits, err := repo.FTS5Search(s.DB, query, limit*3)
	if err != nil {
		return nil, err
	}

	bm25Scores := make(map[string]float64)
	var allIDs []string
	seen := make(map[string]bool)
	for _, h := range bm25Hits {
		bm25Scores[h.CandidateID] = h.BM25Score
		if !seen[h.CandidateID] {
			seen[h.CandidateID] = true
			allIDs = append(allIDs, h.CandidateID)
		}
	}

	vecScores := make(map[string]float64)
	var vecHits []repo.SearchHit
	if len(queryVector) > 0 {
		blob := repo.Float64ToBlob(queryVector)
		var err error
		vecHits, err = repo.Vec0Search(s.DB, blob, limit*2)
		if err != nil {
			s.Logger.Warn("vec0 search failed, falling back to BM25 only", "error", err)
		} else {
			for _, h := range vecHits {
				vecScores[h.CandidateID] = h.VecScore
				if !seen[h.CandidateID] {
					seen[h.CandidateID] = true
					allIDs = append(allIDs, h.CandidateID)
				}
			}
		}
	}

	info, err := repo.SnapshotsByCandidateIDs(s.DB, allIDs)
	if err != nil {
		return nil, err
	}

	useFusion := len(vecScores) > 0

	bm25Rank := make(map[string]int) // 1-based rank by BM25 score (lower=better)
	vecRank := make(map[string]int)  // 1-based rank by vec distance (lower=better)

	if useFusion {
		sort.Slice(bm25Hits, func(i, j int) bool { return bm25Hits[i].BM25Score < bm25Hits[j].BM25Score })
		for i, h := range bm25Hits {
			bm25Rank[h.CandidateID] = i + 1
		}
		sort.Slice(vecHits, func(i, j int) bool { return vecHits[i].VecScore < vecHits[j].VecScore })
		for i, h := range vecHits {
			vecRank[h.CandidateID] = i + 1
		}
	}

	var pointers []TalentPointer
	for _, cid := range allIDs {
		si, ok := info[cid]
		if !ok {
			continue
		}
		p := TalentPointer{
			CandidateID: cid,
			NodeID:      si.NodeID,
			EndpointURL: si.EndpointURL,
			DisplayName: si.DisplayName,
			LastSync:    si.IngestedAt,
			IsActive:    si.IsActive,
		}
		if score, ok := bm25Scores[cid]; ok {
			p.BM25Score = score
		}
		if score, ok := vecScores[cid]; ok {
			p.VecScore = score
		}
		pointers = append(pointers, p)
	}

	if useFusion {
		k := 60.0
		sort.Slice(pointers, func(i, j int) bool {
			ri := rrfScore(k, bm25Rank[pointers[i].CandidateID], vecRank[pointers[i].CandidateID])
			rj := rrfScore(k, bm25Rank[pointers[j].CandidateID], vecRank[pointers[j].CandidateID])
			return ri > rj
		})
	} else {
		sort.Slice(pointers, func(i, j int) bool {
			return pointers[i].BM25Score < pointers[j].BM25Score
		})
	}

	if len(pointers) > limit {
		pointers = pointers[:limit]
	}
	for i := range pointers {
		pointers[i].Rank = i + 1
	}

	return &SearchResult{Candidates: pointers}, nil
}

func rrfScore(k float64, bm25Rank, vecRank int) float64 {
	var score float64
	if bm25Rank > 0 {
		score += 1 / (k + float64(bm25Rank))
	}
	if vecRank > 0 {
		score += 1 / (k + float64(vecRank))
	}
	return score
}

func (s *SearchService) GetTalentProfile(candidateID string) (*TalentProfile, error) {
	snap, err := repo.GetSnapshotByCandidate(s.DB, candidateID)
	if err != nil {
		return nil, err
	}
	if snap == nil {
		return nil, nil
	}

	return &TalentProfile{
		CandidateID: snap.CandidateID,
		NodeID:      snap.NodeID,
		EndpointURL: snap.EndpointURL,
		Payload:     snap.PayloadJSON,
		Signature:   hex.EncodeToString(snap.Signature),
		PublicKey:   hex.EncodeToString(snap.PublicKey),
		IngestedAt:  snap.IngestedAt,
	}, nil
}

type TalentProfile struct {
	CandidateID string `json:"candidate_id"`
	NodeID      string `json:"node_id"`
	EndpointURL string `json:"endpoint_url"`
	Payload     string `json:"payload"`
	Signature   string `json:"signature"`
	PublicKey   string `json:"public_key"`
	IngestedAt  int64  `json:"ingested_at"`
}
