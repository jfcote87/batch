// Copyright 2015 James Cote and Liberty Fund, Inc.
// All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
package batch_test

import (
	"github.com/jfcote87/api/googleapi"
	"github.com/jfcote87/batch"
	"io/ioutil"
	"log"
	"net/http"

	cal "google.golang.org/api/calendar/v3"
	gmail "google.golang.org/api/gmail/v1"
	storage "google.golang.org/api/storage/v1"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/jwt"
)

type DatabaseEvent struct {
	Id          string `json:"id"`
	Title       string `json:"nm"`
	Start       string `json:"start"`
	End         string `json:"end"`
	Description string `json:"desc"` // full description
	Link        string `json:"link"` // web link to full event info
	Loc         string `json:"loc"`  // Event location
}

func ExampleService_calendar() {
	var calendarId string = "xxxxxxxxxxxxx@group.calendar.google.com"
	var dbEvents []DatabaseEvent = getEventData()
	var oauthClient *http.Client = getOauthClient()
	// Read through slice of EventFromDb and add to batch.  Then call Do() to send
	// and process responses
	bsv := batch.Service{Client: oauthClient}
	calsv, _ := cal.New(googleapi.BatchClient())

	for _, dbEvent := range dbEvents {
		// create Event
		event := &cal.Event{
			Summary:            dbEvent.Title,
			Description:        dbEvent.Description,
			Location:           dbEvent.Loc,
			Start:              &cal.EventDateTime{DateTime: dbEvent.Start},
			End:                &cal.EventDateTime{DateTime: dbEvent.End},
			Reminders:          &cal.EventReminders{UseDefault: false},
			Transparency:       "transparent",
			Source:             &cal.EventSource{Title: "Web Link", Url: dbEvent.Link},
			ExtendedProperties: &cal.EventExtendedProperties{Shared: map[string]string{"DBID": dbEvent.Id}},
		}

		event, err := calsv.Events.Insert(calendarId, event).Do()
		// queue new request in batch service
		if err = bsv.Queue(err, dbEvent, batch.ServiceCredentials); err != nil {
			log.Println(err)
			return
		}
	}

	// execute batch
	results, err := bsv.Do()
	if err != nil {
		log.Println(err)
		return
	}
	for _, r := range results {
		var event *cal.Event
		tag := r.Tag.(DatabaseEvent)
		if r.Err != nil {
			log.Printf("Error adding event (Id: %s %s): %v", tag.Id, tag.Title, r.Err)
			continue
		}
		event = r.Value.(*cal.Event)
		updateDatabaseorSomething(tag, event.Id)
	}
	return
}

func updateDatabaseorSomething(ev DatabaseEvent, newCalEventId string) {
	// Logic for database update or post processing
	return
}

func getEventData() []DatabaseEvent {
	// do something to retrieve data from a database
	return nil
}

func getOauthClient() *http.Client {
	return nil
}

func ExampleService_userdata() {
	projectId, usernames, config := getInitialData()
	// Retrieve the list of available buckets for each user for a given api project as well as
	// profile info for each person
	bsv := batch.Service{} // no need for client as individual requests will have their own authorization
	storagesv, _ := storage.New(googleapi.BatchClient())
	gsv, _ := gmail.New(googleapi.BatchClient())
	config.Scopes = []string{gmail.MailGoogleComScope, "email", storage.DevstorageRead_onlyScope}

	for _, u := range usernames {
		// create new credentials for specific user
		tConfig := *config
		tConfig.Subject = u + "@example.com"
		cred := &batch.Oauth2Credentials{TokenSource: tConfig.TokenSource(oauth2.NoContext)}

		// create bucket list request
		_, err := storagesv.Buckets.List(projectId).Do()
		if err = bsv.Queue(err, []string{u, "Buckets"}, cred); err != nil {
			log.Printf("Error adding bucklist request for %s: %v", u, err)
			return
		}

		// create profile request
		_, err = gsv.Users.GetProfile(u + "@example.com").Do()
		if err = bsv.Queue(err, []string{u, "Profile"}, cred); err != nil {
			log.Printf("Error adding profile request for %s: %v", u, err)
			return
		}
	}

	// execute batch
	results, err := bsv.Do()
	if err != nil {
		log.Println(err)
		return
	}
	// process responses
	for _, r := range results {
		tag := r.Tag.([]string)
		if r.Err != nil {
			log.Printf("Error retrieving user (%s) %s: %v", tag[0], tag[1], r.Err)
			continue
		}
		if tag[1] == "Profile" {
			profile := r.Value.(*gmail.Profile)
			log.Printf("User %s profile id is %d", profile.EmailAddress, profile.HistoryId)
		} else {
			blist := r.Value.(*storage.Buckets)
			log.Printf("User: %s", tag[0])
			for _, b := range blist.Items {
				log.Printf("%s", b.Name)
			}
		}
	}
	return
}

func getInitialData() (string, []string, *jwt.Config) {
	jwtbytes, _ := ioutil.ReadFile("secret.json")

	config, _ := google.JWTConfigFromJSON(jwtbytes)
	return "XXXXXXXXXXXXXXXX", []string{"jcote", "bclinton", "gbush", "bobama"}, config
}
