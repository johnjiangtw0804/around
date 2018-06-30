package main

import (
	"cloud.google.com/go/storage"
	"context"
	"encoding/json"
	"fmt"
	"github.com/pborman/uuid"
	elastic "gopkg.in/olivere/elastic.v3"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	// Autho and Golang
	"github.com/auth0/go-jwt-middleware"
	"github.com/dgrijalva/jwt-go"
	"github.com/gorilla/mux"

	//BIg Table
	"cloud.google.com/go/bigtable"
)

const (
	DISTANCE = "200km"
	TYPE     = "post"
	INDEX    = "around"
	// Needs to update
	PROJECT_ID  = "stunning-symbol-206502"
	BT_INSTANCE = "around-post"
	// Needs to update this URL if you deploy it to cloud.
	ES_URL      = "http://35.224.32.198:9200"
	BUCKET_NAME = "post-images-206502-0804"
	API_PREFIX  = "/api/v1"
)

type Location struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type Post struct {
	// `json:"user"` is for the json parsing of this User field.
	// Otherwise, by default it's 'User';
	User     string   `json:"user"`
	Message  string   `json:"message"`
	Location Location `json:"location"`
	Url      string   `json: "url"`
	Type     string   `json:"type"`
	Face     float64  `json:"face"`
}

var (
	mediaTypes = map[string]string{
		".jpeg": "image",
		".jpg":  "image",
		".gif":  "image",
		".png":  "image",
		".mov":  "video",
		".mp4":  "video",
		".avi":  "video",
		".flv":  "video",
		".wmv":  "video",
	}
)

var mySigningKey = []byte("secret")

func main() {
	// Create a client
	client, err := elastic.NewClient(elastic.SetURL(ES_URL),
		elastic.SetSniff(false))
	if err != nil {
		panic(err)
		return
	}

	// Use the indexExists service to check if a specified index exists
	exists, err := client.IndexExists(INDEX).Do()
	if err != nil {
		panic(err)
	}

	if !exists {
		// Create a new index
		mapping := `{
						"mappings":{
							   "post":{
									"properties":{
										"location":{
											"type":"geo_point"
									}
								}
							}
						}	

		}`
		_, err := client.CreateIndex(INDEX).Body(mapping).Do()
		if err != nil {
			panic(err)
		}
	}

	fmt.Println("started-service")

	// Here we are instantiating the gorila/mux router, r is a router
	r := mux.NewRouter()

	// Create a new JWT middleware with a Option that uses the key ‘mySigningKey
	var jwtMiddleware = jwtmiddleware.New(jwtmiddleware.Options{
		ValidationKeyGetter: func(token *jwt.Token) (interface{}, error) {
			return mySigningKey, nil
		},
		SigningMethod: jwt.SigningMethodHS256,
	})

	r.Handle(API_PREFIX+"/post", jwtMiddleware.Handler(http.HandlerFunc(handlerPost))) // not just post, front end might use option to test the function so remove Method function
	r.Handle(API_PREFIX+"/search", jwtMiddleware.Handler(http.HandlerFunc(handlerSearch)))
	r.Handle(API_PREFIX+"/cluster", jwtMiddleware.Handler(http.HandlerFunc(handlerCluster)))
	r.Handle(API_PREFIX+"/login", http.HandlerFunc(loginHandler))
	r.Handle(API_PREFIX+"/signup", http.HandlerFunc(signupHandler))

	// Backend endpoints.
	http.Handle(API_PREFIX+"/", r)
	// Frontend endpoints.
	http.Handle("/", http.FileServer(http.Dir("build")))

	log.Fatal(http.ListenAndServe(":8080", nil))

}

func handlerSearch(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received one search request")
	lat, _ := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lon, _ := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	// range is optional
	ran := DISTANCE
	if val := r.URL.Query().Get("range"); val != "" {
		ran = val + "km"
	}

	fmt.Printf("Search received: %f %f %s\n", lat, lon, ran)

	// Create a client
	client, err := elastic.NewClient(elastic.SetURL(ES_URL),
		elastic.SetSniff(false))
	if err != nil {
		panic(err)
		return
	}

	// Define geo distance query as specified in
	// https://www.elastic.co/guide/en/elasticsearch/reference/5.2/
	// query-dsl-geo-distance-query.html
	q := elastic.NewGeoDistanceQuery("location")
	q = q.Distance(ran).Lat(lat).Lon(lon)

	searchResult, err := client.Search().
		Index(INDEX).Query(q).Pretty(true).
		Do()

	if err != nil {
		// Handle error
		panic(err)
	}

	// searchResult is of type SearchResult and returns hits, suggestions,and all kinds of other information from Elasticsearch.
	fmt.Printf("Query took %d milliseconds\n", searchResult.TookInMillis)
	// TotalHits is another convenience function that works even
	// somethings goes wrong
	fmt.Printf("Found a total of %d post\n", searchResult.TotalHits())

	// Each is a convenience function that iterates over hits in a
	// search result
	// it makes sure you dont need to check for nil values in the
	// response.
	// However, it ignores errors in serialization.
	var typ Post
	var ps []Post

	for _, item := range searchResult.Each(reflect.TypeOf(typ)) {
		// instance of
		p := item.(Post) // p = (Post) item
		fmt.Printf("Post by %s: %s at last %v and lon %v\n", p.User,
			p.Message, p.Location.Lat, p.Location.Lon)

		if !containsFilterWords(&p.Message) {
			ps = append(ps, p)
		}
	}

	js, err := json.Marshal(ps)
	if err != nil {
		panic(err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(js)
}

func handlerPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow_Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")

	user := r.Context().Value("user")
	claims := user.(*jwt.Token).Claims
	username := claims.(jwt.MapClaims)["username"]

	// After you call ParseMultipartForm, the file will be saved in the server memory with maxMemory size.
	// If the file size is larger than maxMemory, the rest of the data will be saved in a system temporary file.
	r.ParseMultipartForm(32 << 20) // 32g

	// Parse form data
	// fmt.Printf("Received one post request %s\n", r.FormValue("message"))
	lat, _ := strconv.ParseFloat(r.FormValue("lat"), 64)
	lon, _ := strconv.ParseFloat(r.FormValue("lon"), 64)

	p := &Post{
		User:    username.(string),
		Message: r.FormValue("message"),
		Location: Location{
			Lat: lat,
			Lon: lon,
		},
	}

	file, _, err := r.FormFile("image") // return a file input stream
	if err != nil {
		http.Error(w, "GCE is not setup", http.StatusInternalServerError)
		fmt.Printf("GCS is not set up %v\n", err)
		panic(err)
	}

	defer file.Close()

	ctx := context.Background() // save to GCS we need a application credentials
	// context can be used to read write the data from the GCS

	id := uuid.New()
	_, attrs, err := saveToGCS(ctx, file, BUCKET_NAME, id) // your file input stream will be empty

	if err != nil {
		http.Error(w, "GCS is not setup", http.StatusInternalServerError)
		fmt.Printf("GCS is not set up %v\n", err)
		panic(err)
	}

	im, header, _ := r.FormFile("image")
	defer im.Close()
	suffix := filepath.Ext(header.Filename)

	// Client needs to know the media type so as to render it.
	if t, ok := mediaTypes[suffix]; ok {
		p.Type = t
	} else {
		p.Type = "unknown"
	}
	// ML Engine only supports jpeg.
	if suffix == ".jpeg" {
		if score, err := annotate(im); err != nil {
			http.Error(w, "Failed to annotate the image", http.StatusInternalServerError)
			fmt.Printf("Failed to annotate the image %v\n", err)
			return
		} else {
			p.Face = score
		}
	}

	p.Url = attrs.MediaLink

	// Save to ES.
	saveToES(p, id)

	// Save to BigTable, just to save data to big table to use BigQuery
	saveToBigTable(p, id)
}

func saveToBigTable(p *Post, id string) {
	ctx := context.Background()
	bt_client, err := bigtable.NewClient(ctx, PROJECT_ID, BT_INSTANCE)
	if err != nil {
		panic(err)
		return
	}

	// open the table
	tbl := bt_client.Open("post")
	mut := bigtable.NewMutation()
	t := bigtable.Now()

	mut.Set("post", "user", t, []byte(p.User)) // p.Uset got converted into byte array
	mut.Set("post", "message", t, []byte(p.Message))
	mut.Set("location", "lat", t, []byte(strconv.FormatFloat(p.Location.Lat, 'f', -1, 64)))
	mut.Set("location", "lon", t, []byte(strconv.FormatFloat(p.Location.Lon, 'f', -1, 64)))

	err = tbl.Apply(ctx, id, mut)
	if err != nil {
		panic(err)
		return
	}
	fmt.Printf("Post is saved to BigTable: %s\n", p.Message)
}

func saveToGCS(ctx context.Context, r io.Reader, bucketName, name string) (*storage.ObjectHandle, *storage.ObjectAttrs, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, nil, err
	}

	bucket := client.Bucket(bucketName)

	if _, err := bucket.Attrs(ctx); err != nil {
		return nil, nil, err
	}

	obj := bucket.Object(name) // id of the object we write
	wc := obj.NewWriter(ctx)   // writer to write on the object

	if _, err = io.Copy(wc, r); err != nil {
		return nil, nil, err
	}

	if err := wc.Close(); err != nil {
		return nil, nil, err
	}

	if err := obj.ACL().Set(ctx, storage.AllUsers, storage.RoleReader); err != nil {
		return nil, nil, err
	}

	attrs, err := obj.Attrs(ctx)

	fmt.Printf("Post is saved to GCS %s\n", attrs.MediaLink)
	return obj, attrs, nil
}

// Save a post to ElasticSearch
func saveToES(p *Post, id string) {
	// Create a client
	es_client, err := elastic.NewClient(elastic.SetURL(ES_URL),
		elastic.SetSniff(false))
	if err != nil {
		panic(err)
		return
	}

	// Save it to index
	_, err = es_client.Index().
		Index(INDEX).
		Type(TYPE).
		Id(id).
		BodyJson(p).
		Refresh(true).
		Do()
	if err != nil {
		panic(err)
		return
	}
	fmt.Printf("Post is saved to index: %s\n", p.Message)
}

func containsFilterWords(s *string) bool {
	filterWords := []string{
		"fuck",
		"pussy",
	}
	for _, word := range filterWords {
		if strings.Contains(*s, word) {
			return true
		}
	}
	return false

}

func handlerCluster(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received one request for clustering")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")

	if r.Method != "GET" {
		return
	}

	term := r.URL.Query().Get("term")

	// Create a client
	client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		http.Error(w, "ES is not setup", http.StatusInternalServerError)
		fmt.Printf("ES is not setup %v\n", err)
		return
	}

	// Range query.
	// For details, https://www.elastic.co/guide/en/elasticsearch/reference/current/query-dsl-range-query.html
	q := elastic.NewRangeQuery(term).Gte(0.9)

	searchResult, err := client.Search().
		Index(INDEX).
		Query(q).
		Pretty(true).
		Do()
	if err != nil {
		// Handle error
		m := fmt.Sprintf("Failed to query ES %v", err)
		fmt.Println(m)
		http.Error(w, m, http.StatusInternalServerError)
	}

	// searchResult is of type SearchResult and returns hits, suggestions,
	// and all kinds of other information from Elasticsearch.
	fmt.Printf("Query took %d milliseconds\n", searchResult.TookInMillis)
	// TotalHits is another convenience function that works even when something goes wrong.
	fmt.Printf("Found a total of %d post\n", searchResult.TotalHits())

	// Each is a convenience function that iterates over hits in a search result.
	// It makes sure you don't need to check for nil values in the response.
	// However, it ignores errors in serialization.
	var typ Post
	var ps []Post
	for _, item := range searchResult.Each(reflect.TypeOf(typ)) {
		p := item.(Post)
		ps = append(ps, p)

	}
	js, err := json.Marshal(ps)
	if err != nil {
		m := fmt.Sprintf("Failed to parse post object %v", err)
		fmt.Println(m)
		http.Error(w, m, http.StatusInternalServerError)
		return
	}

	w.Write(js)
}
