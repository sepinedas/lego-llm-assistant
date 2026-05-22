#!/bin/bash

go build .
mv lego-llm-assistant ~/

systemctl --user daemon-reload
systemctl --user stop lego-maya.service
systemctl --user start lego-maya.service
