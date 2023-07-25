
# 🤖 Cody - Your AI Coding Assistant 

Welcome to Cody! An AI assistant designed to let you interactively query your codebase using natural language. By utilizing vector embeddings, chunking, and OpenAI's language models, Cody can help you navigate through your code in an efficient and intuitive manner. 💻

![image](https://github.com/ajhous44/cody/assets/42582780/f2a62a20-663c-4ec1-b000-67257331fb12)
## LINK
https://www.loom.com/share/eba1d0dcee20430fbd412580d1c0ea0e?sid=4998cf6f-45b4-480d-b742-6f22f3a49dc3


Cody continuously updates its knowledge base every time you save a file, ensuring you have the most up-to-date information. You can customize your setup by specifying directories to ignore in the `ignore_list`.

## 🚀 Getting Started

1. Set the environment variable `OPENAI_API_KEY` in a `.env` file with your OpenAI API key.
2. Modify the `ignore_list` in the `if __name__ == "__main__":` section of the script to specify directories and files you wish to exclude from monitoring.
3. Run the script using Python: python cody.py

   4. Once the script is running, type 'Q' and press enter to switch to question mode. Cody is ready to answer your queries!

## 🎯 Features

- **File Monitoring**: Real-time monitoring of all files in your project's directory and subdirectories. 👀
- **Embedding-based Knowledge Base**: Create a knowledge base using OpenAI Embeddings. Cody collects the contents of all text and JSON files and adds them to this knowledge base. 📚
- **Interactive Querying**: Listen to user inputs. Ask questions, and Cody will generate a response using the knowledge base. 🧠
- **Customizable**: Easily specify files or directories to ignore during monitoring.

## 🛠 Dependencies

- `dotenv`: Load variables from a `.env` file into the environment.
- `langchain`: A language processing library used for text splitting and embeddings.
- `watchdog`: Monitor filesystem events in real-time.
- `openai`: Generate smart responses using OpenAI's language model.

## 💡 Usage

To query your codebase, type 'Q' and press enter. Cody will prompt you to input your question. Once you've entered your query, Cody will generate a response based on its knowledge base.

To stop the script, type 'exit' and press enter.

## ⚠️ Notes & Tips

- Cody uses the FAISS library for efficient similarity search in storing vectors. Please ensure you have sufficient memory available, especially when monitoring a large number of files.
- Additionally, be sure to monitor your OpenAI api usage. A helpful tip is to set a monthly spend limit inside of your OpenAI account to prevent anything crazy from happening. As an additional helper, it prints the number of tokens used in each call you make.
- "LIVE" coding questions. To use to it's full potential. I recommend opening a seperate terminal or even command prompt cd'ing into your project directory, and then launching python cody.py. Then place it split screen with your code in a small viewing window on the far left or right. This way, you can use a seperate terminal for actually running your code without worrying about Cody or having to run him (er... it) each time! This will still continue to update with each file save you do on any file so it always is using the latest data.


Happy Coding with Cody! 💡🚀🎉
