# Revs README

## User authentication
1. Users generates DSA public/private key
2. User ID is SHA256(?) of DSA public key
3. The public key associated with an ID is published. 
4. All user documents are hashed and signed.
5. Receiver can verify that a id/public key set are matched per #2.
6. Receiver can verify document is valid by checking hash and running DSA verification.

## Distribution
* servers
* cdns

## References
* http://dataprotocols.org/couchdb-replication/
* https://www.socketloop.com/tutorials/golang-example-for-dsa-functions

## Name Ideas
drf (Distributed Ratings Format/File) for schema  
Ratex  
PRF Portable Rating Format  

### Rejects
Cinerate
Ratings bazaar

## Movie/TV Data Sources
* https://www.omdbapi.com
* https://www.themoviedb.org
* https://www.imdb.com

# Feature Ideas
* “No Rating” / Abstaining. Actively not rating a movie you’ve seen is a reasonable thing. The most common situation I’ve run into where I want to do this is when the movie seems well done, and that I should like it, but there is something from keeping me from being engaged. I’ve got something on my mind, or I really don’t feel like watching **any** movie. In this case, a “No Rating” seems appropriate.

# Advocacy
* “I want a movie that both Angela and I will like that is less than 90 minutes”
* Separate ratings for kids
* Choose to keep ratings for local or privacy is a big deal. Download ratings and get recommendations locally. 
* It’s less about the accuracy of the algorithms then it is the flexibility of the tool.
* You don’t need a database 400 million ratings to be useful. I remember a time when I would get useful ratings from a best friend, or sneak previews on PBS.
* Content is more plentiful than ever,across multiple platforms. Need help to avoid wasting time reentering ratings is a waste of time. 
* Complex queries like  Show reviews from people who have also seen XYZ
* Joe might have great insights on his blog that you'll never find.
* No need to keep up with streams like Twitter and Facebook.
* Whenever I run into another ratings website, my first thought is: here we go again. The website is going to kiss you to enter a lot of my permission, probably stuff that you've entered 20 times before, and very likely years it will go away. The thing is, what I am proposing is not at all a threat to such initiatives. In fact, it will help them. How often do I read comments about such sites where someone is asking, isn't there someway I could import my data from another site. Well, with my system you would be able to. Just enter your public key, and you're done.
* Some systems pride themselves on letting you get your data out, but that implies that your data was at some point in. With what I'm proposing, your data is always out, and various systems can simply use it.	
* Want allow rapid innovation on the client and algorithm side.
* Rather than just hoping that the service you use offers and export option, this application will be intrinsically exported all of the time.
* What did Netflix or IMDB do with all those ratings. What about the great Netflix algorithms? Try to get arbitrary ratings out.
* Try exporting from these services
* Ratings are a public expression, locked up in silos, used for profit.
* http://mobile.nytimes.com/2008/11/23/magazine/23Netflix-t.html
* Collaborative filtering is good, but so is seeing a single person's rating history, especially when it aligns well with yours.    Considered being told:  Fred rated "The Godfather Part III" 1 star.  Now what if you also learn:  Fred rated both "The Godfather" and "The Godfather Part II" 5 stars.
* Following an individual user could be interesting, and fun.  You'll never know if you're being followed, and visa-versa.
* Part of saving the web:  https://medium.com/matter/the-web-we-have-to-save-2eb1fe15a426
* Reluctant to write reviews because of the effort involved in fear that they'll just disappear someday.
* https://fivethirtyeight.com/features/fandango-movies-ratings/  (https://news.ycombinator.com/item?id=10397200)
* Life is too short for Transformers. Expose people to a world of media. Avoid the hype of the day and latest crazes. 
* Much better experience.  IMDb used to be an email system. Now it's is a multimedia extravaganza. Go to any movie site db you'll get much the same. 
* Could add feature like limiting movies to what you can actually get (http://www.streamingobserver.com/netflix-now-31-movies-imdbs-top-250-list/)
* The man who stood up to facebook  http://www.npr.org/sections/alltechconsidered/2016/10/13/497820170/the-man-who-stood-up-to-facebook
* If there’s a movie I’m going to love, I want to heard about it even if it’s not on Netflix, Google Play, or whatever store/service I happen to belong to.
* Decoupling reviews and recommendations from the provider is a good thing. It's not hard to filter by provider. 
* Benefit : set your own rating range, and customization in general
* Discussion of Netflix/IMDB article: https://news.ycombinator.com/item?id=12692215
* Whereas the has been emphasis on APIs and opening up their data, this project there is just data and it's all open.
* Two core goals: Decouple ratings from providers, Liberate ratings data. Distributed and free for anyone to use, study or add to.
* Compare to ipfs and camlistore and Wikipedia and basic Web and project Guttenberg . Say how even ftp could work.
* Comment on how recommendations are terribly influence by other factors that big corporations have
* I see reviews tucked away in sites and wonder who will ever find them.
* Why should I change systems. I like my website. Petition site to consume ratings
* http://spectrum.ieee.org/computing/software/the-million-dollar-programming-prize
* What.cd shutdown reaction  https://news.ycombinator.com/item?id=12981736
* http://saintlad.com/why-netflix-never-used-algorithm-won-netflix-1-million-challenge/
* http://techblog.netflix.com/2012/04/netflix-recommendations-beyond-5-stars.html?m=1
* Have a similarity threshold for movies that can be adjusted
* http://lenskit.org/
* https://booklens.umn.edu/about
* Selection bias. It could be a good thing if only a few people use this. The masses are stupid and do things like give the RT app a bad rating because of the movie selection. Also, I look for life advice on Hacker News.
* In the spirit of openness simplicity is a goal. The average programmer should be able to write a basic client or even server that interacts with the system.
* Great motivation from Maciej, championing old technology and how the Internet allows niche interests to flourish: http://idlewords.com/talks/web_design_first_100_years.htm
* Reluctant to write reviews because of the effort involved in fear that they'll just disappear someday.
* https://musicbrainz.org/
* https://news.ycombinator.com/item?id=14516538  (what are you doing about the closed web?)
* https://freecinc.com   Taskwarrior free sync service. Just for motivation… another free sync service
* Analogy: health data shared between apps on my iPhone 




# Design

## Schema
JSON, YAML, XML, etc. file that would contain all of a persons movie ratings including:
* name/nick
* demographic (m/f, age, country)
* email
* UUID
* web address of this file
* For each movie:
		* Title
		* Year
		* Viewing
				* Date watched
				* Rating (0-100)
						*    ——  or ——
				* Review
				* Spoiler flag
				* Status: “W” wishlist, “A” avoid list
		* Notes

Internally any thing can be stored like imdb ids or artwork or wikipedia links. Published data should be minimal.

## JSON Signing
* Use a modified version of Camlistore’s signing scheme.  https://camlistore.googlesource.com/camlistore/+/master/doc/json-signing/
* Asymmetric authentication is done using Adam Langley’s ed25519 library. Every user will generate a public/private key pair.
* Serialize object to JSON
* Remove trailing whitespace and convert to UNIX line endings “\n"
* JSON must have a “user_id” field containing the users public key in ascii hex format, lower case, without any delimeters
* Remove last paren ‘}’ from string
* Calculate ed25519 signature of this string
* Append a base64 version of the signature to the string like: ,"signature":"..."

## File Format
### Principles
* Simple
* Readable
* Extensible
* Standardized/machine readable
* Concise


Don't include stuff like Likes in official record. Not a popularity contest. Avoid everyone reading the top reviews? Or maybe not... Dunno
https://en.m.wikipedia.org/wiki/Reputation_system

### Naming
http://www.imdb.com/czone/?ref_=nv_cm_cz_2
http://www.imdb.com/updates/guide/title_formats

### More on format
Choosing a file format is difficult because there competing goals, such as:

* Readability
* Parsability
* Extensibility
* At rest size
* In-transit size
* Diff-ability (how well can partial updates be sent or stored)

Some options, such as a custom binary format, were ruled out almost immediately. Many of the others were tested with sample data. There best compromise was reached, not too surprisingly, with bzip2-compressed JSON. 

Custom

Base format hierarchical or not?
Messagepack, protobufs 
Is a cust

Bzip2 is awesome

Remember Rich Hickey’s "The Value of Values”. Explicit is better than references and offsets.

## Interchange
* https://ssbc.github.io/scuttlebutt-protocol-guide/
### Server
Not a mirror!

### Title data
_External ids_

### Profile data
External IDs
IMDb? Netflix?

### Scenarios 
*  And assumption is that all servers will be trying to keep up to date with everything. What if, however, one server flags way behind on almost all files, but does have the most recent versions of a few files? How does the client void maintaining state of a bunch of old stuff just to track new stuff.

## Delegation and revocation
Originally I was coming up with complicated schemes to authorize a third party to publish ratings on behalf of another.  Instead, let's be more optimistic.  Give your private key out if you wish, but be use some common sense. Most sites will be fine. BUT... come up with a revocation scheme. Something like:

* Generate A. Save and never give out the revocation token.
* I'm publishing under A and give out my private keys
* Some time later I see unauthorized updates to A
* Publish B that includes a revocation of A.
* Clients consumers of B will stop using A

**Method**
A basic rule will be if a secret key is ever published, the ID should be ignored. So revocation of A can be done be simply published the private key of A in B.


# References
* https://movielens.org/profile/about-your-ratings
* http://letterboxd.com
* http://www.phi-phenomenon.org
* http://web.onetel.com/~hibou/morse/MORSE.html
* https://en.m.wikipedia.org/wiki/Reputation_system
* ftp://ftp.fu-berlin.de/pub/misc/movies/database/diffs/README
* http://archive.org/stream/GuerillaOpenAccessManifesto/Goamjuly2008_djvu.txt
* [Hashing javascript objects](https://github.com/puleos/object-hash/blob/master/index.js)
* https://joinmastodon.org/
* http://beets.readthedocs.io/
* http://iipc.github.io/warc-specifications/specifications/warc-format/warc-1.0/index.html
* https://rsync.samba.org/how-rsync-works.html
* http://www.omdbapi.com
* https://news.ycombinator.com/item?id=15931721
* http://clutchofthedeadhand.com/roundup-2017/
* https://en.wikipedia.org/wiki/Template:IMDb_title
* https://datatransferproject.dev
* https://news.ycombinator.com/item?id=17574707
* https://news.ycombinator.com/item?id=18226101
* https://tox.chat


#dev/revs
