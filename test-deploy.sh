#!/bin/bash

set -e

TMPDIR=$(mktemp -d)

echo "<html><head><link rel='stylesheet' href='./styles.css'/></head><body><h1>It works!</h1></body></html>" > $TMPDIR/index.html
echo "h1 { color: red; }" > $TMPDIR/styles.css
tar -czvf content.tar.gz -C $TMPDIR .  
curl --data-binary @content.tar.gz "http://forge-pages.test:8080/deploy?repo=testuser/proj1"

rm -rf $TMPDIR content.tar.gz

#echo "Done! Page should be available at http://testuser.forge-pages.test:8080/proj1/"
