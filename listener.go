package main

import (
	"bytes"
	"code.google.com/p/go.crypto/ripemd160"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
)

func handleCreatingPlayerPassVerify(world metaManager, c net.Conn, playerName string, newPass []byte) {
	defer func() {
		for i := range newPass {
			newPass[i] = 0
		}
	}()
	playerName = strings.ToLower(playerName)
	c.Write([]byte("Please verify your password.\n"))
	passVerify, err := getBytesSecure(c)
	defer func() {
		for i := range passVerify {
			passVerify[i] = 0
		}
	}()
	if err != nil {
		return
	}
	if bytes.Compare(newPass, passVerify) != 0 {
		c.Write([]byte("The passwords you entered do not match.\n"))
		go handleCreatingPlayerPass(world, c, playerName)
		return
	}
	fmt.Println("creating player")
	salt := make([]byte, 128)
	n, err := rand.Read(salt)
	if n != len(salt) || err != nil {
		fmt.Println("Error creating salt.")
		c.Close()
		return
	}
	saltedPass := make([]byte, len(salt)+len(newPass))
	saltedPass = append(saltedPass, salt...)
	saltedPass = append(saltedPass, newPass...)

	h := ripemd160.New()
	h.Write(saltedPass)
	hashedPass := h.Sum(nil)

	world.players.createPlayer(player_state{
		name:        playerName,
		pass:        hashedPass,
		passthesalt: salt,
		connection:  c,
		level:       1,
	})
	world.playerLocations.addPlayer(playerName, roomIdentifier(0))
	player, exists := world.players.getPlayer(playerName)
	if !exists {
		fmt.Println("handleCreatingPlayerPassVerify error: newly created player does not exist: " + playerName)
		c.Close()
		return
	}
	world.players.changePlayer(player.Name(), func(p *player_state) {
		p.connection = c
		p.health = p.MaxHealth()
		p.mana = p.MaxMana()
	})
	go handlePlayer(world, player.Id())
	return
}

func handleCreatingPlayerPass(world metaManager, c net.Conn, player string) {
	c.Write([]byte("Please enter a password for your character.\n"))
	pass, err := getBytesSecure(c)
	if err != nil {
		return
	}
	go handleCreatingPlayerPassVerify(world, c, player, pass)
}

func handleCreatingPlayer(world metaManager, c net.Conn, player string) {
	const playerCreateMessage = "No player by that name exists. Do you want to create a player?"
	c.Write([]byte(playerCreateMessage + "\n"))
	createReply, err := getString(c)
	if err != nil {
		return
	}
	if strings.HasPrefix(createReply, "y") {
		go handleCreatingPlayerPass(world, c, player)
		return
	}
	go handleLogin(world, c)
}

func handleLoginPass(world metaManager, c net.Conn, playerName string) {
	playerName = strings.ToLower(playerName)
	c.Write([]byte("Please enter your password.\n"))
	pass, err := getBytesSecure(c)
	defer func() {
		for i := range pass {
			pass[i] = 0
		}
	}()
	if err != nil {
		return
	}
	player, exists := world.players.getPlayer(playerName)
	if !exists {
		return // we just validated the player exists, so this shouldn't happen
	}
	saltedPass := make([]byte, len(player.passthesalt)+len(pass))
	saltedPass = append(saltedPass, player.passthesalt...)
	saltedPass = append(saltedPass, pass...)
	defer func() {
		for i := range saltedPass {
			saltedPass[i] = 0
		}
	}()
	h := ripemd160.New()
	h.Write(saltedPass)
	hashedPass := h.Sum(nil)
	if !bytes.Equal(hashedPass, player.pass) {
		c.Write([]byte("Invalid password.\n"))
		c.Close()
		return
	}
	world.players.changePlayer(player.Name(), func(p *player_state) {
		p.connection = c
	})
	go handlePlayer(world, player.Id())
}

// this handles new connections, which are not yet logged in
func handleLogin(world metaManager, c net.Conn) {
	negotiateTelnet(c)
	const loginMessageString = "Please enter your name:\n"
	c.Write([]byte(loginMessageString))
	for {
		player, error := getString(c)
		if error != nil {
			return
		}
		player = strings.ToLower(player)
		const validNameRegex = "^[a-zA-Z]+$" // names can only contain letters
		valid, err := regexp.MatchString(validNameRegex, player)
		if err != nil || !valid {
			const invalidNameMessage = "That is not a valid name. Please enter your name."
			c.Write([]byte(invalidNameMessage + "\n"))
			continue
		}

		// if the current player doesn't exist, assume the sent text is the player name
		_, playerExists := world.players.getPlayer(player)
		if !playerExists { // player wasn't found
			go handleCreatingPlayer(world, c, player)
			return
		}
		go handleLoginPass(world, c, player)
		return
	}

}

// this handles connections for a logged-in player
func handlePlayer(world metaManager, playerId identifier) {
	player, exists := world.players.getPlayerById(playerId)
	if !exists {
		fmt.Println("handlePlayer error: player not found " + playerId.String())
		return
	}
	player.Write("Welcome " + ToProper(player.Name()) + "!")
	look(playerId, &world)

	for {
		message, error := getString(player.connection)
		if error != nil {
			return
		}

		messageArgs := strings.Split(message, " ")
		var trimmedMessageArgs []string // accomodates extra spaces between args
		for _, arg := range messageArgs {
			trimmedArg := strings.Trim(arg, " ")
			if trimmedArg != "" {
				trimmedMessageArgs = append(trimmedMessageArgs, trimmedArg)
			}
		}

		if len(trimmedMessageArgs) == 0 {
			player.Write(commandRejectMessage + "1")
			continue
		}
		commandString := strings.ToLower(trimmedMessageArgs[0])
		if len(trimmedMessageArgs) > 1 {
			trimmedMessageArgs = trimmedMessageArgs[1:]
		}
		if command, commandExists := commands[commandString]; !commandExists {
			player.Write(commandRejectMessage + "2")
		} else {
			command(trimmedMessageArgs, playerId, &world)
		}
	}
}

/// when concatenating byte arrays, overwrites old bytes.
/// use for getting secure text, such as passwords.
func getBytesSecure(c net.Conn) ([]byte, error) {
	readBuf := make([]byte, 8)
	finalBuf := make([]byte, 0)
	for {
		n, err := c.Read(readBuf)
		if err != nil {
			if err != io.EOF {
				fmt.Println("error: " + err.Error())
				return nil, err
			} else {
				fmt.Println("connection closed0.")
				return nil, errors.New("connection closed")
			}
		}
		readBuf = bytes.Trim(readBuf[:n], "\x00")
		if len(readBuf) == 0 {
			continue
		}
		readBuf = handleTelnet(readBuf, c)
		if len(readBuf) == 0 {
			continue
		}
		finalBuf = append(finalBuf, readBuf...)
		if len(finalBuf) == 0 {
			continue
		}
		if finalBuf[len(finalBuf)-1] == '\n' {
			break
		}
	}
	for i := range readBuf {
		readBuf[i] = 0
	}
	finalBuf = bytes.Trim(finalBuf, " \r\n")
	return finalBuf, nil
}

// this returns a string sent by the connected client.
// it also processes any Telnet commands it happens to read
func getString(c net.Conn) (string, error) {
	//debug
	fi, _ := os.Create("rec")
	defer fi.Close()

	readBuf := make([]byte, 8)
	finalBuf := make([]byte, 0)
	for {
		n, err := c.Read(readBuf)
		if err != nil {
			if err != io.EOF {
				fmt.Println("error: " + err.Error())
				return "", err
			} else {
				fmt.Println("connection closed1.")
				return "", errors.New("connection closed")
			}
		}
		readBuf = bytes.Trim(readBuf[:n], "\x00")
		if len(readBuf) == 0 {
			continue
		}
		readBuf = handleTelnet(readBuf, c)
		if len(readBuf) == 0 {
			continue
		}
		finalBuf = append(finalBuf, readBuf...)
		if len(finalBuf) == 0 {
			continue
		}
		if finalBuf[len(finalBuf)-1] == '\n' {
			break
		}
	}
	finalBuf = bytes.Trim(finalBuf, " \r\n")
	fi.WriteString(string(finalBuf))
	fmt.Println("read " + strconv.Itoa(len(finalBuf)) + " bytes: B" + string(finalBuf) + "B")
	return string(finalBuf), nil
}

// listen for new connections, and spin them off into goroutines
func listen(world metaManager) {
	port := defaultPort
	if len(os.Args) > 1 {
		argPort, success := strconv.Atoi(os.Args[1])
		if success != nil {
			port = defaultPort
		} else if port < 0 || port > 65535 {
			port = defaultPort
		} else {
			port = argPort
		}
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		fmt.Println("error: " + err.Error())
		return
	}
	fmt.Println("running at " + ln.Addr().String())
	for {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Println(err)
			continue
		}
		conn.Write([]byte("Welcome to gomud. "))
		go handleLogin(world, conn)
	}
}
