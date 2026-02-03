import os
import discord
from discord import app_commands

TOKEN = os.environ["DISCORD_TOKEN"]
GUILD_ID = os.environ["SERVER_ID"]

class Bot(discord.Client):
    def __init__(self):
        intents = discord.Intents.default()
        super().__init__(intents=intents)
        self.tree = app_commands.CommandTree(self)

    async def setup_hook(self):
        guild = discord.Object(id=GUILD_ID)
        self.tree.copy_global_to(guild=guild)
        await self.tree.sync(guild=guild)

bot = Bot()

@bot.event
async def on_ready():
    print(f"Logged in as {bot.user} (ID: {bot.user.id})")

    # KEEPING YOUR ACTIVITY EXACTLY
    await bot.change_presence(
        status=discord.Status.dnd,
        activity=discord.Game(
            name="CLCTR MULTITHREAD PROCESSOR ACTIVATED"
        )
    )

# ---------------- SLASH COMMANDS ----------------

@bot.tree.command(name="ping", description="Check if BIOCOM is alive")
async def ping(interaction: discord.Interaction):
    await interaction.response.send_message(
        "BIOCOM: STATUS OPERATIONAL. GLOBAL LOOP.",
        ephemeral=True
    )

@bot.tree.command(name="intercept", description="intercept communication")
@app_commands.describe(
    message="Post a message to the specified channel or thread",
    channel="Optional channel or thread (defaults to current)"
)
async def intercept(
    interaction: discord.Interaction,
    message: str,
    channel: discord.abc.GuildChannel | discord.Thread | None = None,
):
    await interaction.response.defer(thinking=True, ephemeral=True)

    target = channel or interaction.channel

    # Safety check
    if not hasattr(target, "send"):
        await interaction.followup.send(
            "BIOCOM: CANNOT TRANSMIT TO TARGET.",
            ephemeral=True
        )
        return

    async with target.typing():
        await target.send(message)

    await interaction.followup.send(
        "BIOCOM: MESSAGE SENT.",
        ephemeral=True
    )

bot.run(TOKEN)
