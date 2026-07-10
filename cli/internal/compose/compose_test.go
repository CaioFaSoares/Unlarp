package compose

import (
	"reflect"
	"testing"
)

func TestParsePSAndPublishedPorts(t *testing.T) {
	// docker compose v2 mais novo: NDJSON (um objeto por linha)
	ndjson := `{"Name":"api-web-1","Service":"web","State":"running","Publishers":[{"URL":"0.0.0.0","TargetPort":8080,"PublishedPort":3000,"Protocol":"tcp"}]}
{"Name":"api-db-1","Service":"db","State":"running","Publishers":[{"URL":"0.0.0.0","TargetPort":5432,"PublishedPort":5432,"Protocol":"tcp"},{"URL":"0.0.0.0","TargetPort":5432,"PublishedPort":5432,"Protocol":"udp"}]}
{"Name":"api-worker-1","Service":"worker","State":"running","Publishers":null}`

	svcs, err := ParsePS(ndjson)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 3 || svcs[0].Service != "web" || svcs[1].State != "running" {
		t.Errorf("parse NDJSON inesperado: %+v", svcs)
	}
	if got := PublishedPorts(svcs); !reflect.DeepEqual(got, []int{3000, 5432}) {
		t.Errorf("PublishedPorts = %v; esperado [3000 5432] (dedup, sem udp, sem não-publicadas)", got)
	}

	// versões que emitem array JSON
	array := `[{"Name":"api-web-1","Service":"web","State":"exited","Publishers":[]}]`
	svcs, err = ParsePS(array)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 || svcs[0].State != "exited" {
		t.Errorf("parse array inesperado: %+v", svcs)
	}
	if got := PublishedPorts(svcs); len(got) != 0 {
		t.Errorf("esperado nenhuma porta, obtido %v", got)
	}

	// saída vazia (nenhum serviço) não é erro
	if svcs, err = ParsePS("  \n"); err != nil || svcs != nil {
		t.Errorf("saída vazia deveria retornar nil,nil: %v %v", svcs, err)
	}
}

func TestCommandFor(t *testing.T) {
	got := CommandFor("/ws/api", "", "up -d")
	if got != "cd '/ws/api' && docker compose up -d" {
		t.Errorf("CommandFor sem -f: %q", got)
	}
	got = CommandFor("/ws/api", "docker-compose.prod.yml", "ps --format json")
	if got != "cd '/ws/api' && docker compose -f 'docker-compose.prod.yml' ps --format json" {
		t.Errorf("CommandFor com -f: %q", got)
	}
}
