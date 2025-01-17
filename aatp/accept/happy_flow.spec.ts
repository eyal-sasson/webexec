import { Buffer } from 'node:buffer';
import { test, expect, Page, BrowserContext } from '@playwright/test'
import { Client } from 'ssh2'
import { getOffer } from '../infra/lib'
import * as fs from 'fs'
import waitPort from 'wait-port'

test.describe('use webexec accept to start a session', ()  => {

    const sleep = (ms) => { return new Promise(r => setTimeout(r, ms)) }

    let page: Page,
        context: BrowserContext

    test.beforeAll(async ({ browser }) => {
        context = await browser.newContext()
        page = await context.newPage()
        page.on('console', (msg) => console.log('console log:', msg.text()))
        page.on('pageerror', (err: Error) => console.log('PAGEERROR', err.message))
        const response = await page.goto("http://client")
        await expect(response.ok()).toBeTruthy()
    })

    test('it can accept an offer and candidates', async () => {
        let cmdClosed = false
        let conn, stream
        let sentCans = []
        try {
            conn = await new Promise((resolve, reject) => {
                const conn = new Client()
                conn.on('error', e => reject(e))
                conn.on('ready', () => resolve(conn))
                conn.connect({
                  host: 'webexec',
                  port: 22,
                  username: 'runner',
                  password: 'webexec'
                })
            })
        } catch(e) { expect(e).toBeNull() }
        // log key SSH events
        console.log("ssh connected")
        conn.on('error', e => console.log("ssh error", e))
        conn.on('close', e => {
            console.log("ssh closed", e)
        })
        conn.on('end', e => console.log("ssh ended", e))
        conn.on('keyboard-interactive', e => console.log("ssh interaction", e))
        try {
            stream = await new Promise((resolve, reject) => {
                conn.exec("webexec accept", { pty: true }, async (err, s) => {
                    if (err)
                        reject(err)
                    else 
                        resolve(s)
                })
            })
        } catch(e) { expect(e).toBeNull() }
        let webexecCan = ""
        stream.on('close', (code, signal) => {
            console.log(`closed with ${signal}`)
            cmdClosed = true
        }).on('data', async (data) => {
            let s
            let b = new Buffer.from(data)
            console.log("DATA: " + b.toString())
            webexecCan += b.toString()
            // remove the CR & LF in the end
            if (webexecCan.slice(-1) == "\n")
                webexecCan = webexecCan.slice(0, -2)
            // ignore the leading READY
            if (webexecCan == "READY") {
                webexecCan = ""
                return
            }
            try {
                s = JSON.parse(webexecCan)
            } catch(e) { return }
            let found = sentCans.indexOf(webexecCan)
            webexecCan = ""
            if (found >= 0) {
                return
            }
            await page.evaluate(async (can) => {
                if (!can)
                    return
                if (can.candidate) {
                    try {
                        await window.pc.addIceCandidate(can)
                    } catch(e) { expect(e).toBeNull() }
                } else {
                    try {
                        await window.pc.setRemoteDescription(can)
                    } catch(e) { expect(e).toBeNull() }
                }


            }, s)
        }).stderr.on('data', (data) => {
              console.log("ERROR: " + data)
        })
        const offer = await getOffer(page)
        sentCans.push(offer)
        stream.write(offer + "\n")
        let pcState = null
        while (pcState != "connected") {
            let cans = []
            try {
                cans = await page.evaluate(() => {
                    ret = window.candidates
                    window.candidates = []
                    return ret
                })
            } catch(e) { expect(e).toBeNull() }
           cans.forEach((c) => {
               const s = JSON.stringify(c)
               stream.write(s+"\n")
               sentCans.push(s)
           })
            try {
                pcState = await page.evaluate(() => window.pc.connectionState)
            } catch(e) { expect(e).toBeNull() }
            await sleep(500)
        }
        while (!cmdClosed) {
            await sleep(500)
        }
        try {
            stream = await new Promise((resolve, reject) => {
                conn.exec("webexec status", { pty: true }, async (err, s) => {
                    if (err)
                        reject(err)
                    else 
                        resolve(s)
                })
            })
        } catch(e) { expect(e).toBeNull() }
        let response = ""
        cmdClosed = false
        stream.on('close', (code, signal) => {
            console.log(`closed with ${signal}`)
            cmdClosed = true
            conn.end()
        }).on('data', async (data) => {
            let s
            let b = new Buffer.from(data)
            console.log("DATA: " + b.toString())
            response += b.toString()
        })
        while (!cmdClosed) {
            await sleep(200)
        }
        expect(response).toMatch("process id")
    })
})
